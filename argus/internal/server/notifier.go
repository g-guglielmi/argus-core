// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

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
	// minHoldGraceSecs is the least a held alert waits after its master recovers (the alert delay if
	// longer): enough for a reconnecting probe's buffered data to close its "no data" problems.
	minHoldGraceSecs   = 90
	notifyPollInterval = 30 * time.Second
	notifyBaselineKey  = "notifier_baseline"
	// Set once the alerts already firing when per-channel delivery tracking arrived were credited to the
	// channels that got them under the old "every matching channel" routing, so they still resolve there.
	notifyDeliveriesKey = "notifier_deliveries_backfilled"
	// Set once the Argus-raised problems present when they were introduced were baselined.
	notifySynthBaselineKey = "notifier_synth_baseline"
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

// notifyDest is one alert destination - a global channel or a personal one - with its routing
// (sites + severity floor), its escalation ("notify after") and reminder ("remind every") timing, and
// a send that records the outcome on the channel's delivery-health line.
type notifyDest struct {
	key     string // store.DeliveryKey
	kind    string // store.DeliveryGlobal | store.DeliveryUser
	id      int64
	sites   []string
	minSev  int
	delay   int64 // seconds
	repeat  int64 // seconds, 0 = no reminders
	remSev  int   // reminders only at or above this severity
	alerts  bool  // carries problem alerts (a channel can carry only system notices)
	notices bool  // carries Argus's system notices
	userID  int64 // a personal channel's owner (0 for a shared channel)
	scoped  bool  // a personal channel of a user limited to some sites (scope.go)
	created int64 // unix s
	send    func(ctx context.Context, ev notify.Event)
}

func (d notifyDest) serves(groups []string, sev int) bool {
	return d.alerts && channelMatches(d.sites, d.minSev, groups, sev)
}

// noSite is a site no host group can be in: a personal channel whose sites don't meet its owner's
// serves it alone, so it serves nothing (an empty list would mean every site).
const noSite = "\x00"

// userRecipient is an active user an email channel set to "registered users" delivers to, with the
// sites they may see.
type userRecipient struct {
	email string
	scope siteScope
}

// reaches reports whether an alert or notice may go to this user: an alert on a host in their sites,
// a notice about one of their sites (a probe's), and a notice about the whole install only when they
// see every site.
func (u userRecipient) reaches(ev notify.Event) bool {
	if u.scope.all {
		return true
	}
	if len(ev.Groups) > 0 {
		return u.scope.sees(ev.Groups)
	}
	return ev.Site != "" && u.scope.coversGroup(ev.Site)
}

// userDirectory is who the notifier can reach and what each user may see (per-site visibility).
type userDirectory struct {
	recipients []userRecipient     // active users, for the email-to-users channels
	scopes     map[int64]siteScope // every user's sites, for their personal channels
}

// loadUserDirectory reads the users once per tick. A user it can't find sees nothing, so a personal
// channel never outlives its owner's limits.
func loadUserDirectory(ctx context.Context, st *store.Store) userDirectory {
	dir := userDirectory{scopes: map[int64]siteScope{}}
	users, err := st.ListUsers(ctx)
	if err != nil {
		return dir
	}
	for i := range users {
		u := &users[i]
		sc := scopeOf(u)
		dir.scopes[u.ID] = sc
		if !u.Disabled {
			dir.recipients = append(dir.recipients, userRecipient{email: u.Email, scope: sc})
		}
	}
	return dir
}

// notifyDests turns the enabled global and personal channels into one destination list. A personal
// channel serves only its owner's sites, whatever sites it was set to.
func notifyDests(st *store.Store, channels []store.NotifyChannel, userChannels []store.UserNotifyChannel, dir userDirectory, logger *slog.Logger) []notifyDest {
	out := make([]notifyDest, 0, len(channels)+len(userChannels))
	for _, c := range channels {
		c := c
		out = append(out, notifyDest{
			key: store.DeliveryKey(store.DeliveryGlobal, c.ID), kind: store.DeliveryGlobal, id: c.ID,
			sites: c.Sites, minSev: c.MinSeverity, delay: int64(c.DelayMin) * 60, repeat: int64(c.RepeatMin) * 60,
			remSev: c.RepeatSev, alerts: c.Alerts, notices: c.Notices, created: c.CreatedAt.Unix(),
			send: func(ctx context.Context, ev notify.Event) { sendGlobal(ctx, st, c, dir.recipients, ev, logger) },
		})
	}
	for _, c := range userChannels {
		c := c
		sc := dir.scopes[c.UserID]
		sites := sc.narrow(c.Sites)
		if !sc.all && len(sites) == 0 {
			sites = []string{noSite}
		}
		out = append(out, notifyDest{
			key: store.DeliveryKey(store.DeliveryUser, c.ID), kind: store.DeliveryUser, id: c.ID,
			sites: sites, minSev: c.MinSeverity, delay: int64(c.DelayMin) * 60, repeat: int64(c.RepeatMin) * 60,
			remSev: c.RepeatSev, alerts: c.Alerts, notices: c.Notices, userID: c.UserID, scoped: !sc.all, created: c.CreatedAt.Unix(),
			send: func(ctx context.Context, ev notify.Event) { sendPersonal(ctx, st, c, ev, logger) },
		})
	}
	return out
}

func notifyTick(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, mgr *settings.Manager, secret string) {
	if !zbx.Authenticated() {
		return
	}
	// Read the live values each tick so a Settings change takes effect without a restart.
	publicURL := mgr.PublicURL()
	loc := mgr.Location()
	flapDelay := mgr.AlertDelay()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	problems, err := zbx.AllProblems(ctx)
	if err != nil {
		logger.Warn("notifier: fetch problems", "err", err)
		return
	}
	notifierRanAt.Store(time.Now().Unix()) // the heartbeat's "alert loop is running" (heartbeat.go)

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
	// Argus-raised problems (a sensor that stopped collecting, an unreachable agent) join Zabbix's. The
	// first time this runs, the ones already present are baselined like a fresh install's problems.
	synth := syntheticProblems(ctx, st, zbx, true)
	recordArgusIncidents(ctx, st, synth) // the incident history keeps what Zabbix doesn't
	if _, done, _ := st.MetaGet(ctx, notifySynthBaselineKey); !done {
		// Every sensor already failing and every interface already down counts as known - including
		// those not yet old enough to be a problem.
		baseline := func(id, name string, sev int) {
			_ = st.UpsertNotifyState(ctx, store.NotifyState{EventID: id, Name: name, Severity: sev, State: "baseline", FirstSeen: time.Now().Unix()})
		}
		for _, p := range synth.problems {
			baseline(p.EventID, p.Name, atoi(p.Severity))
		}
		for id := range synth.silent {
			baseline(id, "not supported", atoi(synthSevUnsupported))
		}
		_ = st.MetaSet(ctx, notifySynthBaselineKey, "2")
	}
	problems, targets = synth.merge(problems, targets)
	hiddenHosts, _ := st.ActiveSuppressionMap(ctx, "hide", "host")
	hiddenItems, _ := st.ActiveSuppressionMap(ctx, "hide", "item")
	acked, _ := st.ActiveSuppressionMap(ctx, "ack", "event")

	// Host -> group names, for per-site channel routing.
	hostGroups := map[string][]string{}
	hosts, herr := zbx.Hosts(ctx)
	if herr == nil {
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
	dests := notifyDests(st, channels, userChannels, loadUserDirectory(ctx, st), logger)
	states, err := st.NotifyStates(ctx)
	if err != nil {
		logger.Warn("notifier: load states", "err", err)
		return
	}
	if _, done, _ := st.MetaGet(ctx, notifyDeliveriesKey); !done {
		backfillDeliveries(ctx, st, states, dests, hostGroups)
		_ = st.MetaSet(ctx, notifyDeliveriesKey, "1")
	}
	deliveries, err := st.NotifyDeliveries(ctx)
	if err != nil {
		logger.Warn("notifier: load deliveries", "err", err)
		return
	}

	// Master sensors: a host's ping (or chosen sensor) and its site's probe hold its other alerts while down.
	masters := loadMasters(ctx, st, zbx, hosts, problems, targets)
	grace := int64(flapDelay / time.Second)
	if grace < minHoldGraceSecs {
		grace = minHoldGraceSecs
	}

	activeIDs := make(map[string]zabbix.Problem, len(problems))
	for _, p := range problems {
		activeIDs[p.EventID] = p
	}

	// Incident starts handed over by alerts that closed on a severity change, keyed by host|item: the
	// problem that took over inherits them, so its "Recovered after" covers the whole incident.
	inherited := map[string]int64{}

	// --- recoveries: fired problems that are no longer active ---
	for eid, stt := range states {
		if _, stillActive := activeIDs[eid]; stillActive {
			continue
		}
		// A sensor that is still not supported but no longer counts as an outage (it never collected):
		// keep its baseline, and drop an alert already sent for it without a recovery notice.
		if synth.silent[eid] {
			if stt.State != "baseline" {
				_ = st.DeleteNotifyState(ctx, eid)
			}
			continue
		}
		// A warning that gives way to its sensor's error trigger (or the reverse) is a severity change,
		// not a recovery: the band triggers ("warn and below high" / "high") hand over on the same
		// sensor, so the old one closing would otherwise send a false RESOLVED mid-incident. RESOLVED
		// goes out only once the sensor has no open problem left. The channels the old alert reached
		// move over to the new one, so they all hear when the incident ends.
		if succ := sensorSuccessor(stt, problems, targets); stt.State == "firing" && succ != "" {
			logger.Info("notifier: severity change on the same sensor, no recovery sent", "event", eid, "host", stt.HostName, "name", stt.Name)
			k := stt.HostID + "|" + stt.ItemID
			if s := incidentStart(stt); inherited[k] == 0 || s < inherited[k] {
				inherited[k] = s
			}
			_ = st.MoveNotifyDeliveries(ctx, eid, succ)
			for key, d := range deliveries[eid] {
				if deliveries[succ] == nil {
					deliveries[succ] = map[string]store.NotifyDelivery{}
				}
				if _, has := deliveries[succ][key]; !has {
					d.EventID = succ
					deliveries[succ][key] = d
				}
			}
		} else if stt.State == "firing" && len(deliveries[eid]) > 0 {
			ev := notify.Event{
				Kind: "recovery", Severity: stt.Severity, State: "ok",
				Host: stt.HostName, Name: stt.Name, Site: primarySite(hostGroups[stt.HostID]), Groups: hostGroups[stt.HostID], When: time.Now().In(loc),
				SinceSecs: time.Now().Unix() - incidentStart(stt), OpenURL: OpenLink(publicURL, stt.HostID, stt.ItemID),
				ChartPNG: alertChart(ctx, zbx, stt.ItemID, "ok"),
			}
			for _, d := range dests {
				if _, got := deliveries[eid][d.key]; got {
					d.send(ctx, ev)
				}
			}
		}
		_ = st.DeleteNotifyState(ctx, eid)
	}

	// --- new / pending -> firing, then escalation, reminders and acknowledged notices ---
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
		groups := hostGroups[hostID]
		alertable := isAlertable(t, hiddenHosts, hiddenItems, acked, p.EventID)
		_, isAcked := acked[p.EventID]
		isNoData := strings.Contains(t.Expression, "nodata(")

		stt, seen := states[p.EventID]
		// The incident began when Zabbix raised this problem - or earlier, if it took over from an alert
		// on the same sensor at another severity.
		start := stt.IncidentStart
		if start == 0 {
			start = atoi64(p.Clock)
		}
		if s := inherited[hostID+"|"+itemID]; s > 0 && (start == 0 || s < start) {
			start = s
			if seen && stt.IncidentStart != start {
				stt.IncidentStart = start
				_ = st.UpsertNotifyState(ctx, stt)
			}
		}
		if !seen {
			_ = st.UpsertNotifyState(ctx, store.NotifyState{
				EventID: p.EventID, HostID: hostID, ItemID: itemID, HostName: hostName, Name: p.Name,
				Severity: sev, State: "pending", FirstSeen: now.Unix(), IncidentStart: start,
			})
			continue
		}
		if stt.State == "baseline" {
			continue
		}
		hv := masters.hold(hostID, masterRefs(t), atoi64(p.Clock), now.Unix())

		if stt.State == "pending" {
			if !alertable {
				continue // acked/hidden/paused: keep waiting quietly
			}
			// Held by a master that is down (or yet to report): stay pending. Once a down master is back,
			// wait out the alert delay again, so readings that settle as the device (or probe)
			// reconnects don't alert on the way.
			if hv.held {
				if hv.down && stt.HeldAt != now.Unix() {
					if stt.HeldAt == 0 {
						logger.Info("notifier: alert held while its master sensor is down", "event", p.EventID, "host", hostName, "name", p.Name, "by", hv.by)
					}
					stt.HeldAt = now.Unix()
					_ = st.UpsertNotifyState(ctx, stt)
				}
				continue
			}
			if stt.HeldAt > 0 && now.Unix()-stt.HeldAt < grace {
				continue
			}
			// A "no data" trigger already waited its own period (that IS its flap guard), so it alerts
			// straight away instead of sitting out the alert delay too - as do Argus-raised problems,
			// which only exist after failed checks (a sensor) or Zabbix's retries (an interface).
			if now.Sub(time.Unix(stt.FirstSeen, 0)) < flapDelay && !isNoData && !isSynthetic(p.EventID) {
				continue // still within the flap-debounce window
			}
			if !anyServes(dests, groups, sev) {
				continue // nobody serves this site+severity yet; stay pending so it alerts once someone does
			}
			// A "no data" alert's incident really began when the data stopped.
			if isNoData && itemID != "" {
				if items, e := zbx.ItemsByIDs(ctx, []string{itemID}); e == nil {
					if lc := atoi64(items[itemID].LastClock); lc > 0 && lc < start {
						start = lc
					}
				}
			}
			firedAt := now.Unix()
			stt = store.NotifyState{
				EventID: p.EventID, HostID: hostID, ItemID: itemID, HostName: hostName, Name: p.Name,
				Severity: sev, State: "firing", FirstSeen: stt.FirstSeen, FiredAt: &firedAt, IncidentStart: start,
			}
			_ = st.UpsertNotifyState(ctx, stt)
		}

		// Firing. Acknowledging stops escalation and reminders; the channels that already got the alert
		// are told once who took it, and hear again if it's un-acknowledged and still open.
		if isAcked {
			if !stt.AckNotified {
				if len(deliveries[p.EventID]) > 0 {
					ev := notify.Event{
						Kind: "ack", Severity: sev, State: severityState(sev),
						Host: hostName, Name: p.Name, Site: primarySite(groups), Groups: groups, When: now.In(loc),
						OpenURL: OpenLink(publicURL, hostID, itemID),
					}
					ev.AckBy, ev.AckNote = ackBy(ctx, st, p.EventID)
					for _, d := range dests {
						if _, got := deliveries[p.EventID][d.key]; got {
							d.send(ctx, ev)
						}
					}
				}
				stt.AckNotified = true
				_ = st.UpsertNotifyState(ctx, stt)
			}
			continue
		}
		if stt.AckNotified {
			stt.AckNotified = false
			_ = st.UpsertNotifyState(ctx, stt)
		}
		if !alertable || hv.held {
			continue // hidden, paused or held by a down master: no escalation or reminders meanwhile
		}

		plan := planDeliveries(dests, deliveries[p.EventID], groups, sev, incidentStart(stt), firedAtOf(stt), now.Unix())
		if len(plan) == 0 {
			continue
		}
		value, units := reading(ctx, zbx, itemID, isNoData, now.In(loc))
		if r, ok := synth.readings[p.EventID]; ok {
			value, units = r, "" // Zabbix's reason says more than the sensor's stale value
		}
		base := notify.Event{
			Kind: "problem", Severity: sev, State: severityState(sev),
			Host: hostName, Name: p.Name, Site: primarySite(groups), Groups: groups, When: time.Unix(atoi64(p.Clock), 0).In(loc),
			Value: value, Threshold: formatThreshold(parseThreshold(t.Expression), units),
			OpenURL: OpenLink(publicURL, hostID, itemID), AckURL: AckLink(publicURL, secret, p.EventID),
			ChartPNG: alertChart(ctx, zbx, itemID, severityState(sev)),
		}
		for _, pd := range plan {
			ev := base
			row := pd.row
			if pd.reminder {
				ev.Kind = "reminder"
				ev.Reminder = row.Reminders + 1
				ev.SinceSecs = now.Unix() - incidentStart(stt)
				row.Reminders++
			} else {
				row = store.NotifyDelivery{EventID: p.EventID, Kind: pd.dest.kind, ChannelID: pd.dest.id, FirstSent: now.Unix()}
			}
			pd.dest.send(ctx, ev)
			row.Severity = sev
			row.LastSent = now.Unix()
			_ = st.UpsertNotifyDelivery(ctx, row)
		}
	}
}

// plannedDelivery is one send the notifier owes a destination this tick: the alert itself (the
// channel's "notify after" is up) or a reminder (its "remind every" is up since the last send).
type plannedDelivery struct {
	dest     notifyDest
	reminder bool
	row      store.NotifyDelivery // the existing delivery (reminders)
}

// planDeliveries decides who hears about an open, unacknowledged, alertable problem this tick.
//
//   - A destination that serves the host's site and severity gets the alert once the incident has been
//     open for its "notify after" delay (never before the problem went live, i.e. passed the alert
//     delay). A destination created after that moment doesn't get it: adding a channel isn't a reason
//     to replay every open problem at it. A destination that got the alert at another severity
//     (the incident escalated or eased on the same sensor) gets the new one straight away.
//   - A destination that already has the alert at this severity gets a reminder once "remind every"
//     has passed since its last send, if the problem is at or above its "remind for" severity.
func planDeliveries(dests []notifyDest, got map[string]store.NotifyDelivery, groups []string, sev int, start, firedAt, now int64) []plannedDelivery {
	var out []plannedDelivery
	for _, d := range dests {
		if !d.serves(groups, sev) {
			continue
		}
		row, has := got[d.key]
		if has && row.Severity == sev {
			if d.repeat > 0 && sev >= d.remSev && now-row.LastSent >= d.repeat {
				out = append(out, plannedDelivery{dest: d, reminder: true, row: row})
			}
			continue
		}
		if !has {
			due := start + d.delay
			if due < firedAt {
				due = firedAt
			}
			if now < due || d.created > due {
				continue
			}
		}
		out = append(out, plannedDelivery{dest: d})
	}
	return out
}

// firedAtOf is when an alert went live (0 when it never did).
func firedAtOf(stt store.NotifyState) int64 {
	if stt.FiredAt != nil {
		return *stt.FiredAt
	}
	return 0
}

// anyServes reports whether any destination serves a site+severity, whatever its delay.
func anyServes(dests []notifyDest, groups []string, sev int) bool {
	for _, d := range dests {
		if d.serves(groups, sev) {
			return true
		}
	}
	return false
}

// backfillDeliveries runs once, when per-channel delivery tracking first starts: alerts already firing
// were sent to every channel that matched their site and severity, so they are recorded as delivered
// there - which is where their reminders and recovery must go.
func backfillDeliveries(ctx context.Context, st *store.Store, states map[string]store.NotifyState, dests []notifyDest, hostGroups map[string][]string) {
	for eid, stt := range states {
		if stt.State != "firing" {
			continue
		}
		at := firedAtOf(stt)
		if at == 0 {
			at = stt.FirstSeen
		}
		for _, d := range dests {
			if d.serves(hostGroups[stt.HostID], stt.Severity) {
				_ = st.UpsertNotifyDelivery(ctx, store.NotifyDelivery{
					EventID: eid, Kind: d.kind, ChannelID: d.id, Severity: stt.Severity, FirstSent: at, LastSent: at,
				})
			}
		}
	}
}

// reading is an alert's current value for the message - the sensor's last value with its units or, for
// a "no data" alert, how long data has been missing ("No data for 4m (since 00:56)") - and the
// sensor's units, for the threshold.
func reading(ctx context.Context, zbx *zabbix.Client, itemID string, noData bool, now time.Time) (string, string) {
	if itemID == "" {
		return "", ""
	}
	items, err := zbx.ItemsByIDs(ctx, []string{itemID})
	if err != nil {
		return "", ""
	}
	it, ok := items[itemID]
	if !ok {
		return "", ""
	}
	if noData {
		return noDataSince(atoi64(it.LastClock), now), ""
	}
	why := collectorReason(ctx, zbx, it.HostID, it.Key, it.LastValue)
	if r, ok := reachabilityReading(it.Key, it.LastValue); ok {
		return withReason(r, why), ""
	}
	if why != "" {
		return withReason(it.LastValue, why), ""
	}
	return notify.FormatReading(it.LastValue, it.Units), it.Units
}

// reachabilityReading words a 1/0 reachability reading (ping, a collector's "reachable"), which reads
// as nothing on its own. ok is false for any other sensor or value.
func reachabilityReading(key, value string) (string, bool) {
	if r, ok := runningReading(key, value); ok {
		return r, true
	}
	if !isReachabilityKey(key) {
		return "", false
	}
	up, down := "Reachable", "Not reachable"
	if key == defaultMasterKey {
		up, down = "Replying to ping", "No reply to ping"
	}
	switch strings.TrimSpace(value) {
	case "0":
		return down, true
	case "1":
		return up, true
	}
	return "", false
}

// formatThreshold gives a parsed threshold (">600") the sensor's units, scaled the way its reading is
// (">600 s", ">90 %", ">1 GB"), so the two read alike.
func formatThreshold(th, units string) string {
	if th == "" || units == "" {
		return th
	}
	i := strings.IndexFunc(th, func(r rune) bool { return r != '<' && r != '>' && r != '=' })
	if i <= 0 {
		return th
	}
	return th[:i] + notify.FormatReading(th[i:], units)
}

// ackBy names who acknowledged an event, with their note, for the acknowledged notice. An ack from the
// signed link in an alert has no user behind it.
func ackBy(ctx context.Context, st *store.Store, eventID string) (string, string) {
	by, note, err := st.AckInfo(ctx, eventID)
	if err != nil {
		return "", ""
	}
	if by == 0 {
		return "the link in an alert", note
	}
	u, err := st.UserByID(ctx, by)
	if err != nil {
		return "", note
	}
	if name := strings.TrimSpace(u.Name + " " + u.Surname); name != "" {
		return name, note
	}
	return u.Email, note
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

// channelMatches reports whether a channel scoped to `sites` (host-group names; empty = all sites) with
// severity floor `min` should receive a problem in `groups` at severity `sev`: the floor must be at or
// below the severity, and either the channel serves all sites or one of its sites is among the host's
// groups. Shared by global and personal channel routing.
func channelMatches(sites []string, min int, groups []string, sev int) bool {
	if min > sev {
		return false
	}
	if len(sites) == 0 {
		return true
	}
	for _, s := range sites {
		for _, g := range groups {
			if siteCovers(s, g) {
				return true
			}
		}
	}
	return false
}

// siteCovers reports whether a channel's site scope `s` covers host-group `g`: an exact match, or `g`
// nested under `s`. Zabbix host groups are '/'-hierarchical, so a probe's root ("site1") covers every
// subgroup under it ("site1/Infrastructure", "site1/Network", …).
func siteCovers(s, g string) bool {
	return g == s || strings.HasPrefix(g, s+"/")
}

// matchingUserChannels returns the personal channels that serve a host's groups and severity.
func matchingUserChannels(channels []store.UserNotifyChannel, groups []string, sev int) []store.UserNotifyChannel {
	var out []store.UserNotifyChannel
	for _, c := range channels {
		if channelMatches(c.Sites, c.MinSeverity, groups, sev) {
			out = append(out, c)
		}
	}
	return out
}

// sendGlobal delivers ev to one global channel and records the outcome on the channel (the
// Notifications cards show "last sent" / "last failure"), so a broken webhook or SMTP password is
// visible in the UI rather than only in the core's log. An email channel set to deliver to registered
// users is fanned out to each active user's address. st may be nil in tests.
func sendGlobal(ctx context.Context, st *store.Store, c store.NotifyChannel, users []userRecipient, ev notify.Event, logger *slog.Logger) {
	var err error
	if c.Type == "email" && c.Config["recipients"] == "users" {
		err = sendEmailToUsers(ctx, c, users, ev, logger)
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

// sendEmailToUsers delivers ev to each active user's registered email as a separate, private message
// (one recipient per send, so no address is exposed to the others). A user limited to some sites gets
// only what is theirs (userRecipient.reaches). It attempts every recipient and returns an error only
// when none succeeded, so one bad address doesn't suppress the rest - the channel's health line then
// flags a failure only on a total outage.
func sendEmailToUsers(ctx context.Context, c store.NotifyChannel, users []userRecipient, ev notify.Event, logger *slog.Logger) error {
	if len(users) == 0 {
		return fmt.Errorf("email: no active users to deliver to")
	}
	var emails []string
	for _, u := range users {
		if u.reaches(ev) {
			emails = append(emails, u.email)
		}
	}
	if len(emails) == 0 {
		return nil // nobody who may see this host (or notice): not a delivery failure
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

// sendPersonal delivers ev to one personal (per-user) channel, recording the outcome on the channel so
// its owner sees their own delivery health. Reuses the leaf notify.Send with the channel's own config.
func sendPersonal(ctx context.Context, st *store.Store, c store.UserNotifyChannel, ev notify.Event, logger *slog.Logger) {
	err := notify.Send(ctx, notify.Channel{ID: c.ID, Type: c.Type, Name: "personal", Enabled: c.Enabled, Config: c.Config}, ev)
	if err != nil {
		logger.Warn("notifier: personal send failed", "channel", c.ID, "user", c.UserID, "type", c.Type, "kind", ev.Kind, "err", err)
	}
	if st != nil {
		if rerr := st.RecordUserNotifyDelivery(ctx, c.ID, err); rerr != nil {
			logger.Warn("notifier: record personal delivery", "channel", c.ID, "err", rerr)
		}
	}
}

func toNotifyChannel(c store.NotifyChannel) notify.Channel {
	return notify.Channel{ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled, Config: c.Config}
}

var thresholdRe = regexp.MustCompile(`([<>]=?)\s*([0-9]+(?:\.[0-9]+)?)`)

// incidentStart is when the incident behind a notifier state began: its recorded start, or - for rows
// from before that was tracked - when Argus first saw the problem.
func incidentStart(stt store.NotifyState) int64 {
	if stt.IncidentStart > 0 {
		return stt.IncidentStart
	}
	return stt.FirstSeen
}

// sensorSuccessor returns the open problem that sits on the same host and sensor as this closed one at
// a different severity - a band trigger escalating (warning -> high) or easing (high -> warning), which
// must not read as a recovery - or "" when there is none.
func sensorSuccessor(stt store.NotifyState, problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) string {
	if stt.HostID == "" || stt.ItemID == "" {
		return ""
	}
	for _, p := range problems {
		if p.EventID == stt.EventID || atoi(p.Severity) == stt.Severity || atoi(p.Severity) < 2 {
			continue
		}
		t := targets[p.ObjectID]
		if len(t.Hosts) == 0 || t.Hosts[0].HostID != stt.HostID {
			continue
		}
		for _, it := range t.Items {
			if it.ItemID == stt.ItemID {
				return p.EventID
			}
		}
	}
	return ""
}

// noDataSince renders a "no data" alert's reading: how long data has been missing and since when,
// as a time today or a date + time otherwise ("No data for 4m (since 00:53)"). lastClock 0 = never.
func noDataSince(lastClock int64, now time.Time) string {
	if lastClock <= 0 {
		return "No data received yet"
	}
	t := time.Unix(lastClock, 0).In(now.Location())
	since := t.Format("Jan 2 15:04")
	if y1, m1, d1 := t.Date(); y1 == now.Year() && m1 == now.Month() && d1 == now.Day() {
		since = t.Format("15:04")
	}
	return "No data for " + notify.FormatDuration(now.Unix()-lastClock) + " (since " + since + ")"
}

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
