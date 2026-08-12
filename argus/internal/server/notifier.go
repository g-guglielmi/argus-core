package server

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

const (
	notifyPollInterval = 30 * time.Second
	notifyDebounce     = 60 * time.Second // a problem must persist this long before it alerts (flap guard)
	notifyBaselineKey  = "notifier_baseline"
)

// StartNotifier runs the alerting loop: it polls Zabbix problems, applies the same
// suppression rules as the Overview (hidden / paused / acknowledged stay quiet), debounces
// flapping, and dispatches problem/recovery notifications to the configured channels.
func StartNotifier(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, publicURL, secret string) {
	ticker := time.NewTicker(notifyPollInterval)
	defer ticker.Stop()
	// Run one tick shortly after start (seeds the baseline), then on the interval.
	first := time.NewTimer(5 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			notifyTick(ctx, st, zbx, logger, publicURL, secret)
		case <-ticker.C:
			notifyTick(ctx, st, zbx, logger, publicURL, secret)
		}
	}
}

func notifyTick(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, publicURL, secret string) {
	if !zbx.Authenticated() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	problems, err := zbx.AllProblems(ctx)
	if err != nil {
		logger.Warn("notifier: fetch problems", "err", err)
		return
	}

	// One-time baseline: on the very first run, record everything currently active as
	// 'baseline' so a fresh install (or a Zabbix already full of problems) doesn't spam.
	if _, done, _ := st.MetaGet(ctx, notifyBaselineKey); !done {
		now := time.Now().Unix()
		for _, p := range problems {
			if atoi(p.Severity) < 2 {
				continue
			}
			_ = st.UpsertNotifyState(ctx, store.NotifyState{
				EventID: p.EventID, Name: p.Name, Severity: atoi(p.Severity),
				State: "baseline", FirstSeen: now,
			})
		}
		_ = st.MetaSet(ctx, notifyBaselineKey, "1")
		logger.Info("notifier: seeded baseline; alerting begins from now")
		return
	}

	// Suppression + attribution context, mirroring handleProblems.
	tids := make([]string, 0, len(problems))
	for _, p := range problems {
		tids = append(tids, p.ObjectID)
	}
	targets, _ := zbx.TriggerTargets(ctx, tids)
	hiddenHosts, _ := st.ActiveSuppressionMap(ctx, "hide", "host")
	hiddenItems, _ := st.ActiveSuppressionMap(ctx, "hide", "item")
	acked, _ := st.ActiveSuppressionMap(ctx, "ack", "event")

	// Host -> group names, for per-site channel routing.
	hostGroups := map[string][]string{}
	if hosts, herr := zbx.Hosts(ctx); herr == nil {
		for _, h := range hosts {
			names := make([]string, 0, len(h.Groups))
			for _, g := range h.Groups {
				names = append(names, g.Name)
			}
			hostGroups[h.HostID] = names
		}
	}

	channels, _ := st.EnabledNotifyChannels(ctx)
	states, err := st.NotifyStates(ctx)
	if err != nil {
		logger.Warn("notifier: load states", "err", err)
		return
	}

	activeIDs := make(map[string]zabbix.Problem, len(problems))
	for _, p := range problems {
		activeIDs[p.EventID] = p
	}

	// --- recoveries: fired problems that are no longer active ---
	for eid, stt := range states {
		if _, stillActive := activeIDs[eid]; stillActive {
			continue
		}
		if stt.State == "firing" {
			since := time.Now().Unix() - stt.FirstSeen
			ev := notify.Event{
				Kind: "recovery", Severity: stt.Severity, State: "ok",
				Host: stt.HostName, Name: stt.Name, Site: primarySite(hostGroups[stt.HostID]), When: time.Now(),
				SinceSecs: since, OpenURL: OpenLink(publicURL, stt.HostID, stt.ItemID),
			}
			dispatch(ctx, channels, hostGroups[stt.HostID], ev, logger)
		}
		_ = st.DeleteNotifyState(ctx, eid)
	}

	// --- new / pending -> firing ---
	now := time.Now()
	for _, p := range problems {
		sev := atoi(p.Severity)
		if sev < 2 { // below Warning never alerts
			continue
		}
		t := targets[p.ObjectID]
		hostID, hostName := "", ""
		if len(t.Hosts) > 0 {
			hostID, hostName = t.Hosts[0].HostID, t.Hosts[0].Name
		}
		itemID := ""
		if len(t.Items) > 0 {
			itemID = t.Items[0].ItemID
		}
		alertable := isAlertable(t, hiddenHosts, hiddenItems, acked, p.EventID)

		stt, seen := states[p.EventID]
		if !seen {
			_ = st.UpsertNotifyState(ctx, store.NotifyState{
				EventID: p.EventID, HostID: hostID, ItemID: itemID, HostName: hostName, Name: p.Name,
				Severity: sev, State: "pending", FirstSeen: now.Unix(),
			})
			continue
		}
		if stt.State != "pending" {
			continue // baseline or already firing
		}
		if !alertable {
			continue // acked/hidden/paused: keep waiting quietly
		}
		if now.Sub(time.Unix(stt.FirstSeen, 0)) < notifyDebounce {
			continue // still within the flap-debounce window
		}
		matches := matchingChannels(channels, hostGroups[hostID])
		if len(matches) == 0 {
			continue // no channel serves this site yet; stay pending so it alerts once one is added
		}
		value := ""
		if itemID != "" {
			if items, e := zbx.ItemsByIDs(ctx, []string{itemID}); e == nil {
				if it, ok := items[itemID]; ok {
					value = strings.TrimSpace(it.LastValue + " " + it.Units)
				}
			}
		}
		ev := notify.Event{
			Kind: "problem", Severity: sev, State: severityState(sev),
			Host: hostName, Name: p.Name, Site: primarySite(hostGroups[hostID]), When: time.Unix(atoi64(p.Clock), 0),
			Value: value, Threshold: parseThreshold(t.Expression),
			OpenURL: OpenLink(publicURL, hostID, itemID), AckURL: AckLink(publicURL, secret, p.EventID),
		}
		sendAll(ctx, matches, ev, logger)
		firedAt := now.Unix()
		_ = st.UpsertNotifyState(ctx, store.NotifyState{
			EventID: p.EventID, HostID: hostID, ItemID: itemID, HostName: hostName, Name: p.Name,
			Severity: sev, State: "firing", FirstSeen: stt.FirstSeen, FiredAt: &firedAt,
		})
	}
}

// isAlertable reports whether a problem should notify: not on a hidden/paused host, not with
// all its sensors hidden, and not acknowledged. Mirrors handleProblems' filtering.
func isAlertable(t zabbix.TriggerTarget, hiddenHosts, hiddenItems, acked map[string]*int64, eventID string) bool {
	if len(t.Hosts) == 0 {
		return false
	}
	h := t.Hosts[0]
	if _, hidden := hiddenHosts[h.HostID]; hidden || h.Status == "1" {
		return false
	}
	if _, isAcked := acked[eventID]; isAcked {
		return false
	}
	if len(t.Items) > 0 {
		allHidden := true
		for _, it := range t.Items {
			if _, ok := hiddenItems[it.ItemID]; !ok {
				allHidden = false
				break
			}
		}
		if allHidden {
			return false
		}
	}
	return true
}

// matchingChannels returns the channels that serve a host's groups ("" site = all sites).
func matchingChannels(channels []store.NotifyChannel, groups []string) []store.NotifyChannel {
	var out []store.NotifyChannel
	for _, c := range channels {
		if c.Site == "" || contains(groups, c.Site) {
			out = append(out, c)
		}
	}
	return out
}

func dispatch(ctx context.Context, channels []store.NotifyChannel, groups []string, ev notify.Event, logger *slog.Logger) {
	sendAll(ctx, matchingChannels(channels, groups), ev, logger)
}

func sendAll(ctx context.Context, channels []store.NotifyChannel, ev notify.Event, logger *slog.Logger) {
	for _, c := range channels {
		if err := notify.Send(ctx, toNotifyChannel(c), ev); err != nil {
			logger.Warn("notifier: send failed", "channel", c.Name, "type", c.Type, "kind", ev.Kind, "err", err)
		}
	}
}

func toNotifyChannel(c store.NotifyChannel) notify.Channel {
	return notify.Channel{ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled, Site: c.Site, Config: c.Config}
}

var thresholdRe = regexp.MustCompile(`([<>]=?)\s*([0-9]+(?:\.[0-9]+)?)`)

// parseThreshold pulls a best-effort threshold (e.g. ">90") from a trigger expression.
// Complex expressions may not match, in which case it returns "" and the value shows alone.
func parseThreshold(expr string) string {
	m := thresholdRe.FindStringSubmatch(expr)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

func primarySite(groups []string) string {
	if len(groups) > 0 {
		return groups[0]
	}
	return ""
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
