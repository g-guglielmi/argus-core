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
	"sync/atomic"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Sensors gone from the device (DESIGN section 7s). Zabbix's discovery switches off a sensor it no
// longer finds and deletes it a week later; it never checks that sensor's triggers again, so a problem
// open on it stays open until the deletion takes it away. The notifier closes such an alert with the
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

// goneHintFn, set by the server, names what a host's lost sensors look renamed to (old item id ->
// new sensor's name), for the notifier's closing words.
var goneHintFn atomic.Pointer[func(ctx context.Context, hostID string) map[string]string]

// goneItemsOf returns the lost sensors among the problems' sensors, by id (nil when none can be).
func goneItemsOf(ctx context.Context, zbx *zabbix.Client, targets map[string]zabbix.TriggerTarget) map[string]zabbix.Item {
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

// goneClosedEvent words the notice that closes a fired alert whose sensor is gone from the device.
func goneClosedEvent(problem, host, site string, groups []string, sev int, it zabbix.Item, renamedTo, openURL string, now time.Time) notify.Event {
	lines := []string{fmt.Sprintf("%s is gone from the device: Zabbix no longer finds it, so this alert can't clear by itself.", it.Name)}
	if renamedTo != "" {
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
	stt store.NotifyState, seen bool, it zabbix.Item, hostID, hostName string, groups []string, publicURL string, now time.Time) {
	sev := atoi(p.Severity)
	if seen && stt.State == "firing" && len(deliveries[p.EventID]) > 0 {
		renamedTo := goneHints(ctx, hostID)[it.ItemID]
		ev := goneClosedEvent(p.Name, hostName, primarySite(groups), groups, sev, it, renamedTo, OpenLink(publicURL, hostID, ""), now)
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
		if renamedTo != "" {
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
	for _, it := range lost {
		lc := atoi64(it.Discovery.LastCheck)
		next[it.ItemID] = lc
		if !baselined || told[it.ItemID] == lc || s.ren.isDecided(it.ItemID) {
			continue
		}
		if now.Sub(time.Unix(lc, 0)) > goneTellAge {
			continue
		}
		byHost[it.HostID] = append(byHost[it.HostID], it)
	}
	if len(byHost) > 0 {
		s.tellGone(ctx, byHost, dests, now)
	}
	// What is no longer lost (deleted, or found again) is forgotten.
	b, _ := json.Marshal(next)
	_ = s.st.MetaSet(ctx, goneToldKey, string(b))
}

// goneDests is every alert channel, shared and personal.
func (s *Server) goneDests(ctx context.Context) []notifyDest {
	channels, _ := s.st.EnabledNotifyChannels(ctx)
	userChannels, _ := s.st.EnabledUserNotifyChannels(ctx)
	return notifyDests(s.st, channels, userChannels, loadUserDirectory(ctx, s.st, time.Now(), s.mgr.Location()), s.logger)
}

// tellGone sends each host's notice.
func (s *Server) tellGone(ctx context.Context, byHost map[string][]zabbix.Item, dests []notifyDest, now time.Time) {
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return
	}
	hiddenHosts, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	hiddenItems, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	tags := effectiveTagNames(ctx, s.st, hosts)
	loc := s.mgr.Location()
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
		hints := s.renameHints(ctx, h.HostID)
		list := make([]goneLost, 0, len(shown))
		for _, it := range shown {
			list = append(list, goneLost{it: it, renamedTo: hints[it.ItemID]})
		}
		ev := goneNoticeEvent(h.Name, primarySite(groups), groups, list, OpenLink(s.mgr.PublicURL(), h.HostID, ""), now.In(loc))
		s.logger.Info("gone sensors: telling", "host", h.Name, "sensors", len(list), "channels", len(to))
		for _, d := range to {
			_ = d.send(ctx, ev)
		}
	}
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
