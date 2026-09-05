package server

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"argus/internal/notify"
	"argus/internal/settings"
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
func StartNotifier(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, mgr *settings.Manager, secret string) {
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
			notifyTick(ctx, st, zbx, logger, mgr, secret)
		case <-ticker.C:
			notifyTick(ctx, st, zbx, logger, mgr, secret)
		}
	}
}

func notifyTick(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, mgr *settings.Manager, secret string) {
	if !zbx.Authenticated() {
		return
	}
	// Read the live values each tick so a Settings change takes effect without a restart.
	publicURL := mgr.PublicURL()
	loc := mgr.Location()
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
	userChannels, _ := st.EnabledUserNotifyChannels(ctx)
	// Only pay for the user-email lookup when an email channel is actually set to fan out to users.
	var userEmails []string
	if anyEmailToUsers(channels) {
		userEmails, _ = st.NotifyUserEmails(ctx)
	}
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
				Host: stt.HostName, Name: stt.Name, Site: primarySite(hostGroups[stt.HostID]), When: time.Now().In(loc),
				SinceSecs: since, OpenURL: OpenLink(publicURL, stt.HostID, stt.ItemID),
				ChartPNG: alertChart(ctx, zbx, stt.ItemID, "ok"),
			}
			dispatch(ctx, st, channels, userChannels, userEmails, hostGroups[stt.HostID], ev, logger)
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
		gMatches := matchingChannels(channels, hostGroups[hostID], sev)
		uMatches := matchingUserChannels(userChannels, hostGroups[hostID], sev)
		if len(gMatches) == 0 && len(uMatches) == 0 {
			continue // nobody serves this site+severity yet; stay pending so it alerts once someone does
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
			Host: hostName, Name: p.Name, Site: primarySite(hostGroups[hostID]), When: time.Unix(atoi64(p.Clock), 0).In(loc),
			Value: value, Threshold: parseThreshold(t.Expression),
			OpenURL: OpenLink(publicURL, hostID, itemID), AckURL: AckLink(publicURL, secret, p.EventID),
			ChartPNG: alertChart(ctx, zbx, itemID, severityState(sev)),
		}
		sendAll(ctx, st, gMatches, userEmails, ev, logger)
		sendUserChannels(ctx, st, uMatches, ev, logger)
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

// channelMatches reports whether a channel serving host-group `site` with severity floor `min` should
// receive a problem in `groups` at severity `sev`: "" site = all sites, and the floor must be at or
// below the severity. Shared by global and personal channel routing.
func channelMatches(site string, min int, groups []string, sev int) bool {
	if min > sev {
		return false
	}
	return site == "" || contains(groups, site)
}

// matchingChannels returns the global channels that serve a host's groups and severity.
func matchingChannels(channels []store.NotifyChannel, groups []string, sev int) []store.NotifyChannel {
	var out []store.NotifyChannel
	for _, c := range channels {
		if channelMatches(c.Site, c.MinSeverity, groups, sev) {
			out = append(out, c)
		}
	}
	return out
}

// matchingUserChannels is matchingChannels for personal (per-user) channels — same site/severity rule.
func matchingUserChannels(channels []store.UserNotifyChannel, groups []string, sev int) []store.UserNotifyChannel {
	var out []store.UserNotifyChannel
	for _, c := range channels {
		if channelMatches(c.Site, c.MinSeverity, groups, sev) {
			out = append(out, c)
		}
	}
	return out
}

// dispatch routes a recovery the same way its problem would have gone: global channels (including the
// email-to-users fan-out) and personal channels that serve this site+severity.
func dispatch(ctx context.Context, st *store.Store, channels []store.NotifyChannel, userChannels []store.UserNotifyChannel, userEmails []string, groups []string, ev notify.Event, logger *slog.Logger) {
	sendAll(ctx, st, matchingChannels(channels, groups, ev.Severity), userEmails, ev, logger)
	sendUserChannels(ctx, st, matchingUserChannels(userChannels, groups, ev.Severity), ev, logger)
}

// sendAll delivers ev to every global channel and records each outcome on the channel (the
// Notifications cards show "last sent" / "last failure"), so a broken webhook or SMTP password is
// visible in the UI rather than only in the core's log. An email channel set to deliver to registered
// users is fanned out to each active user's address. st may be nil in tests.
func sendAll(ctx context.Context, st *store.Store, channels []store.NotifyChannel, userEmails []string, ev notify.Event, logger *slog.Logger) {
	for _, c := range channels {
		var err error
		if c.Type == "email" && c.Config["recipients"] == "users" {
			err = sendEmailToUsers(ctx, c, userEmails, ev, logger)
		} else {
			err = notify.Send(ctx, toNotifyChannel(c), ev)
			if err != nil {
				logger.Warn("notifier: send failed", "channel", c.Name, "type", c.Type, "kind", ev.Kind, "err", err)
			}
		}
		if st != nil {
			if rerr := st.RecordNotifyDelivery(ctx, c.ID, err); rerr != nil {
				logger.Warn("notifier: record delivery", "channel", c.Name, "err", rerr)
			}
		}
	}
}

// sendEmailToUsers delivers ev to each active user's registered email as a separate, private message
// (one recipient per send, so no address is exposed to the others). It attempts every recipient and
// returns an error only when none succeeded, so one bad address doesn't suppress the rest — the
// channel's health line then flags a failure only on a total outage.
func sendEmailToUsers(ctx context.Context, c store.NotifyChannel, emails []string, ev notify.Event, logger *slog.Logger) error {
	if len(emails) == 0 {
		return fmt.Errorf("email: no active users to deliver to")
	}
	nc := toNotifyChannel(c)
	cfg := make(map[string]string, len(nc.Config)+1)
	for k, v := range nc.Config {
		cfg[k] = v
	}
	nc.Config = cfg
	var firstErr error
	sent := 0
	for _, addr := range emails {
		cfg["to"] = addr
		if err := notify.Send(ctx, nc, ev); err != nil {
			logger.Warn("notifier: send failed", "channel", c.Name, "type", "email", "to", addr, "kind", ev.Kind, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent++
	}
	if sent == 0 {
		return firstErr
	}
	return nil
}

// sendUserChannels delivers ev to personal (per-user) channels, recording each outcome on the channel
// so a user sees their own delivery health. Reuses the leaf notify.Send with the channel's own config.
func sendUserChannels(ctx context.Context, st *store.Store, channels []store.UserNotifyChannel, ev notify.Event, logger *slog.Logger) {
	for _, c := range channels {
		err := notify.Send(ctx, notify.Channel{ID: c.ID, Type: c.Type, Name: "personal", Enabled: c.Enabled, Site: c.Site, Config: c.Config}, ev)
		if err != nil {
			logger.Warn("notifier: personal send failed", "channel", c.ID, "user", c.UserID, "type", c.Type, "kind", ev.Kind, "err", err)
		}
		if st != nil {
			if rerr := st.RecordUserNotifyDelivery(ctx, c.ID, err); rerr != nil {
				logger.Warn("notifier: record personal delivery", "channel", c.ID, "err", rerr)
			}
		}
	}
}

// anyEmailToUsers reports whether any channel is an email channel set to deliver to registered users
// (so notifyTick only loads the user-email list when it's actually needed).
func anyEmailToUsers(channels []store.NotifyChannel) bool {
	for _, c := range channels {
		if c.Type == "email" && c.Config["recipients"] == "users" {
			return true
		}
	}
	return false
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
