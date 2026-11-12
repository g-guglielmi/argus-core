// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Sensors gone from the device (DESIGN section 7s). Zabbix's discovery switches off a sensor it no
// longer finds and deletes it a week later; it never checks that sensor's triggers again, so a problem
// open on it stays open until the deletion takes it away. Gone means lost AND switched off: a rule
// whose table lists only what runs (Windows services) keeps a lost sensor on, reading Down, because
// there lost means stopped; only a name that appeared briefly (Windows lists a service under its
// internal name for a moment after a reboot) is switched off by Argus, as gone. The notifier closes such an alert with the
// reason instead of reminding about it for a week and then sending a RESOLVED that isn't true, and the
// channels hear once, per host, which sensors went and which look renamed.

// stateGone is a notifier state: the problem's sensor is gone from the device, so it never alerts (a
// firing alert was closed with the reason). It goes back to pending if the sensor comes back.
const stateGone = "gone"

// goneTellAge is how recently a sensor must have been lost to be told of (older ones were known
// before this ran: a restart after a long stop, or the first run).
const goneTellAge = 2 * 24 * time.Hour

// goneToldKey is the meta row of the lost sensors already told of: item id -> the lastcheck it was
// lost at (a sensor lost again later, after coming back, is told again).
const goneToldKey = "gone_sensors_told"

// goneReportedKey (meta) holds the sensors a notice reported gone (item id -> host id), so that one
// found again is told back.
const goneReportedKey = "gone_sensors_reported"

// goneHintFn, set by the server, names what a host's lost sensors look renamed to (old item id ->
// new sensor's name), for the notifier's closing words.
var goneHintFn atomic.Pointer[func(ctx context.Context, hostID string) map[string]string]

// lostItemsOf returns the lost sensors among the problems' sensors, by id (nil when none can be): on
// or switched off (switchedOff keeps the gone ones).
func lostItemsOf(ctx context.Context, zbx *zabbix.Client, targets map[string]zabbix.TriggerTarget) map[string]zabbix.Item {
	var ids []string
	seen := map[string]bool{}
	for _, t := range targets {
		for _, it := range t.Items {
			if it.Flags == "4" && !seen[it.ItemID] {
				seen[it.ItemID] = true
				ids = append(ids, it.ItemID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	lost, err := zbx.LostItems(ctx, ids)
	if err != nil {
		return nil
	}
	return lost
}

// switchedOff keeps the lost sensors Zabbix (or Argus) switched off for good: gone from the device.
func switchedOff(lost map[string]zabbix.Item, now time.Time) map[string]zabbix.Item {
	out := map[string]zabbix.Item{}
	for id, it := range lost {
		if it.Status == "1" && goneSettled(it, now) {
			out[id] = it
		}
	}
	return out
}

// goneSettle is how long a sensor must have been lost before it counts as gone. A device that
// reboots can list nothing for a moment (its SNMP agent still starting, a table not filled yet): a
// discovery round that runs then loses every sensor it makes and Zabbix switches them off, and the
// next round finds them again and switches them back on. A loss that outlasts a discovery round or
// two (they run hourly at most) is real.
const goneSettle = 3 * time.Hour

// goneSettled reports whether a lost sensor has been lost for goneSettle, counted from when its
// discovery last found it. Windows services are switched off only by Argus, once it is sure (a name
// listed briefly), so theirs is settled.
func goneSettled(it zabbix.Item, now time.Time) bool {
	if lostMeansStopped(it.Key) {
		return true
	}
	lc := atoi64(it.Discovery.LastCheck)
	return lc <= 0 || now.Sub(time.Unix(lc, 0)) >= goneSettle
}

// lostMeansStopped reports whether a sensor's discovery lists only what runs, so losing it means it
// stopped: its sensor stays on and reads Down (the template never switches it off).
func lostMeansStopped(key string) bool { return keyBase(key) == "win.service.state" }

const (
	// artifactRanHours: a sensor that read running in fewer hours than this was never a real one.
	artifactRanHours = 2
	// artifactHold is how long a young sensor's alert waits for its discovery to drop it (it runs
	// hourly): a name listed for a moment after a reboot never alerts.
	artifactHold = 75 * time.Minute
	// artifactsKey is the meta row of the lost sensors checked: item id -> true for a brief name
	// Argus switched off (hidden, never told of), false for a real one.
	artifactsKey = "gone_artifacts"
)

var ranCache = struct {
	sync.Mutex
	m map[string]ranEntry
}{m: map[string]ranEntry{}}

type ranEntry struct {
	hours int
	at    time.Time
}

// ranHours is how many hours in the last 8 days a sensor read running (hourly trends above 0), or -1
// when its readings can't be read. Kept 10 minutes.
func ranHours(ctx context.Context, zbx *zabbix.Client, itemID string, now time.Time) int {
	ranCache.Lock()
	if e, ok := ranCache.m[itemID]; ok && now.Sub(e.at) < 10*time.Minute {
		ranCache.Unlock()
		return e.hours
	}
	ranCache.Unlock()
	rows, err := zbx.TrendRows(zabbix.WithoutPast(ctx), []string{itemID}, now.Add(-8*24*time.Hour).Unix(), now.Unix())
	if err != nil {
		return -1
	}
	n := 0
	for _, r := range rows {
		if fz(r.ValueAvg) > 0 {
			n++
		}
	}
	ranCache.Lock()
	for id, e := range ranCache.m {
		if now.Sub(e.at) >= 10*time.Minute {
			delete(ranCache.m, id)
		}
	}
	ranCache.m[itemID] = ranEntry{hours: n, at: now}
	ranCache.Unlock()
	return n
}

// briefly reports whether a sensor only ever read running briefly (a name listed for a moment).
func briefly(ctx context.Context, zbx *zabbix.Client, itemID string, now time.Time) bool {
	h := ranHours(ctx, zbx, itemID, now)
	return h >= 0 && h < artifactRanHours
}

// loadArtifacts reads the lost sensors checked (artifactsKey).
func loadArtifacts(ctx context.Context, st *store.Store) map[string]bool {
	out := map[string]bool{}
	if raw, ok, _ := st.MetaGet(ctx, artifactsKey); ok {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func saveArtifacts(ctx context.Context, st *store.Store, m map[string]bool) {
	b, _ := json.Marshal(m)
	_ = st.MetaSet(ctx, artifactsKey, string(b))
}

// switchOffArtifact switches off a lost sensor that only appeared briefly, so it reads as gone
// everywhere, and notes it (hidden, never told of). It reports whether it did.
func switchOffArtifact(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger, itemID string) bool {
	if err := zbx.SetItemEnabled(ctx, itemID, false); err != nil {
		logger.Warn("gone sensors: switch off a brief name", "item", itemID, "err", err)
		return false
	}
	m := loadArtifacts(ctx, st)
	m[itemID] = true
	saveArtifacts(ctx, st, m)
	logger.Info("gone sensors: switched off a name its device listed only briefly", "item", itemID)
	return true
}

// goneTarget reports whether every sensor of a problem's trigger is gone from the device: nothing can
// clear it any more.
func goneTarget(t zabbix.TriggerTarget, gone map[string]zabbix.Item) bool {
	if len(t.Items) == 0 || len(gone) == 0 {
		return false
	}
	for _, it := range t.Items {
		if _, ok := gone[it.ItemID]; !ok {
			return false
		}
	}
	return true
}

// allLost reports whether there are sensors and every one is in lost.
func allLost(ids []string, lost map[string]zabbix.Item) bool {
	if len(ids) == 0 || len(lost) == 0 {
		return false
	}
	for _, id := range ids {
		if _, ok := lost[id]; !ok {
			return false
		}
	}
	return true
}

// goneClosedEvent words the notice that closes a fired alert whose sensor is gone from the device (or
// was only listed briefly: brief).
func goneClosedEvent(problem, host, site string, groups []string, sev int, it zabbix.Item, renamedTo string, brief bool, openURL string, now time.Time) notify.Event {
	lines := []string{fmt.Sprintf("%s is gone from the device: Zabbix no longer finds it, so this alert can't clear by itself.", it.Name)}
	if brief {
		lines = []string{fmt.Sprintf("%s was only listed briefly (a name the device showed for a moment, as Windows does after a reboot) and is gone: Argus switched it off, so this alert can't clear by itself.", it.Name)}
	} else if renamedTo != "" {
		lines = append(lines, fmt.Sprintf("It looks renamed to %s. Link the two on the host page to keep its history.", renamedTo))
	} else if del := atoi64(it.Discovery.TSDelete); del > 0 {
		lines = append(lines, "Zabbix deletes it with its history on "+time.Unix(del, 0).In(now.Location()).Format("Jan 2")+".")
	}
	return notify.Event{
		Kind: "info", State: "ok", Severity: sev, Host: host, Site: site, Groups: groups,
		Name: "Alert closed: " + problem, Detail: strings.Join(lines, "\n"), When: now, OpenURL: openURL,
	}
}

// closeGone takes a problem whose sensor is gone from the device out of alerting: a fired alert is
// closed with the reason at the channels that got it (one still waiting in the outbox is dropped);
// one not yet sent never goes. Its state becomes stateGone, its channel records cleared, so if the
// sensor comes back the problem alerts afresh.
func closeGone(ctx context.Context, st *store.Store, logger *slog.Logger, dests []notifyDest,
	deliveries map[string]map[string]store.NotifyDelivery, pending outboxPending, p zabbix.Problem,
	stt store.NotifyState, seen bool, it zabbix.Item, brief bool, hostID, hostName string, groups []string, publicURL string, now time.Time) {
	sev := atoi(p.Severity)
	if seen && stt.State == "firing" && len(deliveries[p.EventID]) > 0 {
		renamedTo := ""
		if !brief {
			renamedTo = goneHints(ctx, hostID)[it.ItemID]
		}
		ev := goneClosedEvent(p.Name, hostName, primarySite(groups), groups, sev, it, renamedTo, brief, OpenLink(publicURL, hostID, ""), now)
		told, trying, dropped := 0, 0, 0
		for _, d := range dests {
			if _, got := deliveries[p.EventID][d.key]; !got {
				continue
			}
			if k := pendingKey(p.EventID, d.key); pending[k] {
				_ = st.DeleteOutboxPair(ctx, p.EventID, d.key) // its alert never got out: nothing to close
				delete(pending, k)
				dropped++
				continue
			}
			if deliver(ctx, st, pending, d, p.EventID, ev) == nil {
				told++
			} else {
				trying++
			}
		}
		why := it.Name + " is gone from the device"
		if brief {
			why = it.Name + " was only listed briefly"
		} else if renamedTo != "" {
			why += ", looks renamed to " + renamedTo
		}
		tlog(ctx, st, p.EventID, "gone", "", "", why+" ("+toldCount(told, trying, dropped)+")", 0)
	}
	logger.Info("notifier: sensor gone from the device, its problem never alerts", "event", p.EventID, "host", hostName, "name", p.Name, "was", stt.State)
	if seen {
		_ = st.DeleteNotifyState(ctx, p.EventID)
	}
	_ = st.UpsertNotifyState(ctx, store.NotifyState{
		EventID: p.EventID, HostID: hostID, ItemID: it.ItemID, HostName: hostName, Name: p.Name,
		Severity: sev, State: stateGone, FirstSeen: now.Unix(),
	})
}

// goneHints asks the server what a host's lost sensors look renamed to (nil without a server).
func goneHints(ctx context.Context, hostID string) map[string]string {
	if f := goneHintFn.Load(); f != nil {
		return (*f)(ctx, hostID)
	}
	return nil
}

// renameHints is goneHintFn: the suggested new sensor's name for each undecided lost sensor of a host.
func (s *Server) renameHints(ctx context.Context, hostID string) map[string]string {
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		return nil
	}
	return pairNames(s.hostRenamePairs(ctx, items))
}

// pairNames maps each pair's old sensor to its suggested new sensor's name.
func pairNames(pairs []renamePair) map[string]string {
	out := map[string]string{}
	for _, p := range pairs {
		for _, o := range p.Options {
			if o.ID == p.New {
				out[p.Old.ID] = o.Name
			}
		}
	}
	return out
}

// startGoneNotices runs the "sensors gone from the device" notices in the background.
func (s *Server) startGoneNotices(ctx context.Context) {
	f := s.renameHints
	goneHintFn.Store(&f)
	go func() {
		first := time.NewTimer(2 * time.Minute) // after the rename index and the notifier's first pass
		defer first.Stop()
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			s.goneTick(c, s.goneDests(c), time.Now())
			cancel()
		}
	}()
}

// goneLost is one lost sensor to tell of.
type goneLost struct {
	it        zabbix.Item
	renamedTo string // "" = gone, no likely new name
}

// goneTick tells, once per host, of the sensors its discovery lost since the last pass: which look
// renamed (and to what), which are just gone. It goes to the channels that would hear if those
// sensors alerted (site, tags, the highest severity of their triggers), as an [INFO].
func (s *Server) goneTick(ctx context.Context, dests []notifyDest, now time.Time) {
	if !s.zbx.Authenticated() {
		return
	}
	lost, err := s.zbx.SwitchedOffLostItems(ctx)
	if err != nil {
		s.logger.Warn("gone sensors: list", "err", err)
		return
	}
	told := map[string]int64{}
	raw, baselined, _ := s.st.MetaGet(ctx, goneToldKey)
	if baselined {
		_ = json.Unmarshal([]byte(raw), &told)
	}
	next := map[string]int64{}
	byHost := map[string][]zabbix.Item{}
	arts := loadArtifacts(ctx, s.st)
	keptArts := map[string]bool{}
	for _, it := range lost {
		lc := atoi64(it.Discovery.LastCheck)
		// A loss still young may be a device rebooting: its next discovery round finds the sensors
		// again. It is told once it settles (not marked told before then).
		young := !goneSettled(it, now)
		if !young || !baselined {
			next[it.ItemID] = lc
		}
		// A name listed only briefly (switched off by Argus, or by Zabbix before the services kept
		// stopped sensors on) is no news.
		if lostMeansStopped(it.Key) {
			brief, checked := arts[it.ItemID]
			if !checked {
				if h := ranHours(ctx, s.zbx, it.ItemID, now); h >= 0 {
					brief, checked = h < artifactRanHours, true
				}
			}
			if checked {
				keptArts[it.ItemID] = brief
			}
			if brief {
				continue
			}
		}
		if !baselined || young || told[it.ItemID] == lc || s.ren.isDecided(it.ItemID) {
			continue
		}
		if now.Sub(time.Unix(lc, 0)) > goneTellAge {
			continue
		}
		byHost[it.HostID] = append(byHost[it.HostID], it)
	}
	reported := map[string]string{}
	if raw, ok, _ := s.st.MetaGet(ctx, goneReportedKey); ok {
		_ = json.Unmarshal([]byte(raw), &reported)
	}
	if len(byHost) > 0 {
		for id, host := range s.tellGone(ctx, byHost, dests, now) {
			reported[id] = host
		}
	}
	s.tellBack(ctx, reported, lost, dests, now)
	if b, err := json.Marshal(reported); err == nil {
		_ = s.st.MetaSet(ctx, goneReportedKey, string(b))
	}
	// What is no longer lost (deleted, or found again) is forgotten.
	saveArtifacts(ctx, s.st, keptArts)
	b, _ := json.Marshal(next)
	_ = s.st.MetaSet(ctx, goneToldKey, string(b))
}

// tellBack tells of the reported sensors their discovery finds again (switched back on), once per
// host, and forgets them; one Zabbix deleted, or someone switched off, is forgotten without a word.
// It edits reported in place.
func (s *Server) tellBack(ctx context.Context, reported map[string]string, lost []zabbix.Item, dests []notifyDest, now time.Time) {
	stillLost := make(map[string]bool, len(lost))
	for _, it := range lost {
		stillLost[it.ItemID] = true
	}
	var ids []string
	for id := range reported {
		if !stillLost[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	standing, err := s.zbx.DiscoveryStandings(ctx, ids)
	if err != nil {
		return // asked again next pass
	}
	byHost := map[string][]zabbix.Item{}
	for _, id := range ids {
		it, ok := standing[id]
		switch {
		case !ok || it.Status != "0": // deleted with its history, or switched off by someone
			delete(reported, id)
		case it.Discovery.Lost(): // lost but still on: not back yet
		default:
			byHost[it.HostID] = append(byHost[it.HostID], it)
			delete(reported, id)
		}
	}
	if len(byHost) > 0 {
		s.tellHosts(ctx, byHost, dests, now, func(h zabbix.Host, groups []string, shown []zabbix.Item, openURL string) notify.Event {
			return backNoticeEvent(h.Name, primarySite(groups), groups, shown, openURL, now.In(s.mgr.Location()))
		})
	}
}

// backNoticeEvent words a host's notice of its sensors found again.
func backNoticeEvent(host, site string, groups []string, list []zabbix.Item, openURL string, now time.Time) notify.Event {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var name string
	var lines []string
	if len(list) == 1 {
		name = "Back on the device: " + list[0].Name
		lines = append(lines, "Zabbix finds it again: it is checked again and keeps its history.")
	} else {
		name = fmt.Sprintf("%d sensors back on the device", len(list))
		for i, it := range list {
			if i == goneListMax {
				lines = append(lines, fmt.Sprintf("and %d more", len(list)-goneListMax))
				break
			}
			lines = append(lines, "Back: "+it.Name)
		}
		lines = append(lines, "Zabbix finds them again: they are checked again and keep their history.")
	}
	return notify.Event{
		Kind: "info", State: "ok", Host: host, Site: site, Groups: groups,
		Name: name, Detail: strings.Join(lines, "\n"), When: now, OpenURL: openURL,
	}
}

// goneDests is every alert channel, shared and personal.
func (s *Server) goneDests(ctx context.Context) []notifyDest {
	channels, _ := s.st.EnabledNotifyChannels(ctx)
	userChannels, _ := s.st.EnabledUserNotifyChannels(ctx)
	return notifyDests(s.st, channels, userChannels, loadUserDirectory(ctx, s.st, time.Now(), s.mgr.Location()), s.logger)
}

// tellGone sends each host's notice of its lost sensors and returns the sensors it told of (item
// id -> host id).
func (s *Server) tellGone(ctx context.Context, byHost map[string][]zabbix.Item, dests []notifyDest, now time.Time) map[string]string {
	return s.tellHosts(ctx, byHost, dests, now, func(h zabbix.Host, groups []string, shown []zabbix.Item, openURL string) notify.Event {
		hints := s.renameHints(ctx, h.HostID)
		list := make([]goneLost, 0, len(shown))
		for _, it := range shown {
			list = append(list, goneLost{it: it, renamedTo: hints[it.ItemID]})
		}
		return goneNoticeEvent(h.Name, primarySite(groups), groups, list, openURL, now.In(s.mgr.Location()))
	})
}

// tellHosts sends each host's notice of some of its sensors (build words it) to the channels that
// would hear those sensors' alerts: the host's site and tags, at the highest severity of their
// triggers (Warning without one), outside their owner's quiet hours. Hidden and paused hosts and
// hidden sensors are left out. It returns the sensors told of (item id -> host id).
func (s *Server) tellHosts(ctx context.Context, byHost map[string][]zabbix.Item, dests []notifyDest, now time.Time,
	build func(h zabbix.Host, groups []string, shown []zabbix.Item, openURL string) notify.Event) map[string]string {
	told := map[string]string{}
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return told
	}
	hiddenHosts, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	hiddenItems, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	tags := effectiveTagNames(ctx, s.st, hosts)
	for _, h := range hosts {
		items := byHost[h.HostID]
		if len(items) == 0 || h.Status != "0" {
			continue
		}
		if _, hidden := hiddenHosts[h.HostID]; hidden {
			continue
		}
		var shown []zabbix.Item
		ids := make([]string, 0, len(items))
		for _, it := range items {
			if _, hidden := hiddenItems[it.ItemID]; !hidden {
				shown = append(shown, it)
				ids = append(ids, it.ItemID)
			}
		}
		if len(shown) == 0 {
			continue
		}
		// The severity they would alert at: the highest of their triggers (a sensor with none, Warning).
		sev := 2
		if trs, err := s.zbx.ItemTriggers(ctx, ids); err == nil {
			for _, list := range trs {
				for _, tr := range list {
					sev = max(sev, tr.Priority)
				}
			}
		}
		groups := make([]string, 0, len(h.Groups))
		for _, g := range h.Groups {
			groups = append(groups, g.Name)
		}
		route := hostRoute{groups: groups, tags: tags[h.HostID]}
		var to []notifyDest
		for _, d := range dests {
			if d.serves(route, sev) && !d.quietFor(sev) {
				to = append(to, d)
			}
		}
		if len(to) == 0 {
			continue
		}
		ev := build(h, groups, shown, OpenLink(s.mgr.PublicURL(), h.HostID, ""))
		s.logger.Info("sensor notice", "host", h.Name, "notice", ev.Name, "channels", len(to))
		for _, d := range to {
			_ = d.send(ctx, ev)
		}
		for _, it := range shown {
			told[it.ItemID] = h.HostID
		}
	}
	return told
}

// goneListMax is how many sensors a notice names before "and N more".
const goneListMax = 10

// goneNoticeEvent words a host's notice of its lost sensors.
func goneNoticeEvent(host, site string, groups []string, list []goneLost, openURL string, now time.Time) notify.Event {
	sort.Slice(list, func(i, j int) bool {
		if (list[i].renamedTo != "") != (list[j].renamedTo != "") {
			return list[i].renamedTo != "" // the renamed first
		}
		return list[i].it.Name < list[j].it.Name
	})
	renamed := 0
	var del int64
	for _, g := range list {
		if g.renamedTo != "" {
			renamed++
		}
		if d := atoi64(g.it.Discovery.TSDelete); d > 0 && (del == 0 || d < del) {
			del = d
		}
	}
	gone := len(list) - renamed
	var name string
	var lines []string
	switch {
	case len(list) == 1 && renamed == 1:
		name = list[0].it.Name + " looks renamed to " + list[0].renamedTo
	case len(list) == 1:
		name = "Gone from the device: " + list[0].it.Name
	case gone == 0:
		name = fmt.Sprintf("%d sensors look renamed", renamed)
	case renamed == 0:
		name = fmt.Sprintf("%d sensors gone from the device", gone)
	default:
		name = fmt.Sprintf("%d sensors look renamed, %d gone from the device", renamed, gone)
	}
	if len(list) > 1 {
		for i, g := range list {
			if i == goneListMax {
				lines = append(lines, fmt.Sprintf("and %d more", len(list)-goneListMax))
				break
			}
			if g.renamedTo != "" {
				lines = append(lines, g.it.Name+", now "+g.renamedTo)
			} else {
				lines = append(lines, "Gone: "+g.it.Name)
			}
		}
	}
	day := ""
	if del > 0 {
		day = time.Unix(del, 0).In(now.Location()).Format("Jan 2")
	}
	switch {
	case renamed > 0 && day != "":
		lines = append(lines, "Review on the host page to keep the history: Zabbix deletes the old sensors on "+day+".")
	case renamed > 0:
		lines = append(lines, "Review on the host page to keep the history.")
	case day != "" && len(list) == 1:
		lines = append(lines, "Zabbix no longer finds it and deletes it with its history on "+day+".")
	case day != "":
		lines = append(lines, "Zabbix no longer finds them and deletes them with their history on "+day+".")
	}
	return notify.Event{
		Kind: "info", State: "ok", Host: host, Site: site, Groups: groups,
		Name: name, Detail: strings.Join(lines, "\n"), When: now, OpenURL: openURL,
	}
}
