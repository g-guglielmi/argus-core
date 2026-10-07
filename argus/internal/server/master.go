// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Master sensors (PRTG-style dependencies). Every host has a master sensor - its ICMP ping by default,
// or another sensor (or none) chosen in its settings. While the master is down, the host's other
// alerts are held: an unreachable device then sends one "unavailable" alert rather than one per
// sensor. A host monitored through a collector (NUT, XAPI, SSH, an HTTP API) also has the collector's
// reachability sensor as a second master: when the service stops but the machine still pings, only
// "monitoring is unreachable" alerts, not every sensor it feeds. On top of that, a probe's own reachability is the master of its whole site: while a probe
// isn't reporting, nothing it monitors alerts (including Zabbix's own per-proxy checks on the Zabbix
// server host), since none of it can be told apart from the probe being gone.

const (
	defaultMasterKey = "icmpping"       // Base Ping's reachability sensor
	probeMasterKey   = "zabbix[uptime]" // the Probe host's reporting sensor (strict nodata triggers)
	// masterWaitSecs is how long a new problem waits for its master to report after it began, so a
	// device going down alerts as "unavailable" rather than through whichever sensor noticed first
	// (ping needs up to 3 failed checks, a minute apart, before its trigger fires).
	masterWaitSecs = 5 * 60
)

// collectorMasterKeys are the Argus templates' collector reachability sensors (1 = the collector
// reaches its target, 0 = it doesn't): each one feeds the rest of its host's sensors.
var collectorMasterKeys = []string{"nut.reachable", "xcp.reachable", "linux.ssh.reachable", "adguard.running", "hass.running"}

// isReachabilityKey reports whether a master's value is a 1/0 reachability (ping or a collector), so
// a fresh 0 means "going down" even before its trigger fires.
func isReachabilityKey(key string) bool {
	if key == defaultMasterKey || strings.HasPrefix(key, "net.tcp.service[") { // ping, a TCP/HTTP(S) service check
		return true
	}
	for _, k := range collectorMasterKeys {
		if key == k {
			return true
		}
	}
	return false
}

// proxyItemRe matches Zabbix's own per-proxy checks ("Zabbix server health" template), whose first key
// parameter is the proxy name: zabbix.proxy.last_seen[proxy-site1].
var proxyItemRe = regexp.MustCompile(`^zabbix\.proxy\.[a-z_.]+\["?([^",\]]+)"?`)

// masterItem is a master sensor's identity and latest reading.
type masterItem struct {
	itemID    string
	key       string
	lastValue string
	lastClock int64
	// rank orders a host's masters: 0 is its main one (the ping, or the sensor chosen in its settings),
	// 1 a collector's reachability sensor. A master holds only what ranks below it.
	rank int
}

// masterSet is everything the notifier needs to decide, per problem, whether a master holds it.
type masterSet struct {
	byHost      map[string][]masterItem // host id -> its master sensors (none = no hold)
	siteMaster  map[string]masterItem   // proxy id -> its Probe host's reporting sensor
	probeHost   map[string]string       // proxy id -> its Probe host id
	hostProxy   map[string]string       // host id -> the proxy monitoring it ("" / "0" = the server)
	proxyByName map[string]string       // proxy name -> proxy id
	down        map[string]bool         // master item id -> it has an open "down" problem
	upstream    map[string]upstreamLink // host id -> the device it is plugged into (upstream.go)
}

// loadMasters builds the master set for one notifier tick. Best effort: whatever can't be read just
// means no hold for the hosts it concerns.
func loadMasters(ctx context.Context, st *store.Store, zbx *zabbix.Client, hosts []zabbix.Host, problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) masterSet {
	m := masterSet{
		byHost: map[string][]masterItem{}, siteMaster: map[string]masterItem{}, probeHost: map[string]string{},
		hostProxy: map[string]string{}, proxyByName: map[string]string{}, down: masterDown(problems, targets),
	}
	m.upstream, _, _ = loadUpstreams(ctx, st, zbx)
	classes, _ := st.DeviceClasses(ctx)
	for _, h := range hosts {
		m.hostProxy[h.HostID] = h.ProxyID
		if classes[h.HostID] == provision.ClassProbe && h.ProxyID != "" && h.ProxyID != "0" {
			m.probeHost[h.ProxyID] = h.HostID
		}
	}
	if proxies, err := zbx.Proxies(ctx); err == nil {
		for _, p := range proxies {
			m.proxyByName[p.Name] = p.ProxyID
		}
	}

	// Default masters: each host's ping and each Probe host's reporting sensor, plus the collector
	// reachability sensors, in one lookup.
	defaults := map[string]masterItem{}
	collectors := map[string][]masterItem{}
	if items, err := zbx.ItemsByKeys(ctx, append([]string{defaultMasterKey, probeMasterKey}, collectorMasterKeys...)); err == nil {
		for _, it := range items {
			if it.Status != "0" || it.State == "1" {
				continue // a disabled or unsupported sensor never reports, so it can't vouch for the host
			}
			mi := masterItem{itemID: it.ItemID, key: it.Key, lastValue: it.LastValue, lastClock: atoi64(it.LastClock)}
			if it.Key != defaultMasterKey && it.Key != probeMasterKey {
				mi.rank = 1
				collectors[it.HostID] = append(collectors[it.HostID], mi)
				continue
			}
			if it.Key == probeMasterKey {
				if classes[it.HostID] != provision.ClassProbe {
					continue
				}
				for proxyID, hostID := range m.probeHost {
					if hostID == it.HostID {
						m.siteMaster[proxyID] = mi
					}
				}
			}
			defaults[it.HostID] = mi // a Probe host has no ping, so its reporting sensor is its master
		}
	}
	overrides, _ := st.HostMasters(ctx)
	var custom []string
	for _, id := range overrides {
		if id != "" {
			custom = append(custom, id)
		}
	}
	customItems, _ := zbx.ItemsByIDs(ctx, custom)
	for _, h := range hosts {
		if id, ok := overrides[h.HostID]; ok {
			if id == "" {
				continue // "none": nothing holds this host's alerts, not even its collector
			}
			if it, found := customItems[id]; found {
				m.byHost[h.HostID] = append(m.byHost[h.HostID], masterItem{itemID: it.ItemID, key: it.Key, lastValue: it.LastValue, lastClock: atoi64(it.LastClock)})
			}
		} else if mi, ok := defaults[h.HostID]; ok {
			m.byHost[h.HostID] = append(m.byHost[h.HostID], mi)
		}
		m.byHost[h.HostID] = append(m.byHost[h.HostID], collectors[h.HostID]...)
	}
	return m
}

// masterDown marks the items with an open problem that means "down": an error-level one (what the app
// shows in red) or a "no data" one at any severity (a Probe host that stopped reporting). A warning like
// packet loss doesn't make a master down.
func masterDown(problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) map[string]bool {
	out := map[string]bool{}
	for _, p := range problems {
		t := targets[p.ObjectID]
		if atoi(p.Severity) < 3 && !strings.Contains(t.Expression, "nodata(") {
			continue
		}
		for _, it := range t.Items {
			out[it.ItemID] = true
		}
	}
	return out
}

// holdVerdict says whether a master holds a problem back: held (don't alert yet) and, when a master is
// actually down (not just yet to report), down - a problem held by a down master waits out the alert
// delay again once the master recovers, since its readings settle only after reconnecting.
type holdVerdict struct {
	held, down bool
	by         string     // which master: "site" or "host", for the log
	master     masterItem // the master that holds it
	host       string     // the master's host (the Probe host for a site hold)
}

// hold decides for one open problem: on host hostID, on the sensors items (ids + keys), begun at start.
func (m masterSet) hold(hostID string, items []masterRef, start, now int64) holdVerdict {
	// The site: the probe monitoring this host - or, for Zabbix's own per-proxy checks, the probe they
	// are about. The Probe host's own alerts are never held by itself.
	proxyID := m.hostProxy[hostID]
	for _, it := range items {
		if mm := proxyItemRe.FindStringSubmatch(it.key); mm != nil {
			if id := m.proxyByName[mm[1]]; id != "" {
				proxyID = id
			}
		}
	}
	if sm, ok := m.siteMaster[proxyID]; ok && m.probeHost[proxyID] != hostID {
		if v := m.judge(sm, items, start, now); v.held {
			v.by, v.master, v.host = "site", sm, m.probeHost[proxyID]
			return v
		}
	}
	// The devices it is plugged into, nearest first: while one is down Argus can't reach this host
	// through it, so the host's alerts wait on that device's main master (its ping). The top of the
	// outage still alerts: its own chain is up, and a chain that loops holds nothing.
	for _, up := range upstreamChain(m.upstream, hostID) {
		for _, um := range m.byHost[up] {
			if um.rank != 0 {
				continue
			}
			if v := m.judge(um, items, start, now); v.held {
				v.by, v.master, v.host = "upstream", um, up
				return v
			}
		}
	}
	// A host's masters hold only what ranks below them: the ping holds the collector's "unreachable"
	// alert and every other sensor, a collector holds the sensors it feeds, and masters of one rank never
	// hold each other. When the whole machine goes down its ping and its collector are both down, and
	// each would otherwise hold the other's alert for good: nothing at all would go out. A collector
	// holds only what it feeds, so never the ping's loss or response time: those measure the network,
	// and a lossy link says something the stopped service doesn't.
	limit := m.rankOf(hostID, items)
	ping := onPing(items)
	for _, hm := range m.byHost[hostID] {
		if hm.rank >= limit || (hm.rank > 0 && ping) {
			continue
		}
		if v := m.judge(hm, items, start, now); v.held {
			v.by, v.master, v.host = "host", hm, hostID
			return v
		}
	}
	return holdVerdict{}
}

// behindFor names the hosts an alert's host holds while it is down: set when the problem is on the
// host's main master (its ping) and other hosts are plugged in behind it.
func (m masterSet) behindFor(hostID string, items []masterRef, names map[string]string) string {
	main := false
	for _, hm := range m.byHost[hostID] {
		if hm.rank != 0 {
			continue
		}
		for _, it := range items {
			if it.id == hm.itemID {
				main = true
			}
		}
	}
	if !main {
		return ""
	}
	hosts := behindHosts(m.upstream, hostID)
	if len(hosts) == 0 {
		return ""
	}
	ns := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if n := names[h]; n != "" {
			ns = append(ns, n)
		}
	}
	return behindText(ns)
}

// onPing reports whether a problem is on the ping's sensors (Base Ping: reachability, loss, response
// time).
func onPing(items []masterRef) bool {
	for _, it := range items {
		if strings.HasPrefix(it.key, defaultMasterKey) {
			return true
		}
	}
	return false
}

// rankOf is the rank of the highest of the host's masters a problem is on, or one below every rank
// when it isn't on a master.
func (m masterSet) rankOf(hostID string, items []masterRef) int {
	limit := 1 << 30
	for _, hm := range m.byHost[hostID] {
		for _, it := range items {
			if it.id == hm.itemID && hm.rank < limit {
				limit = hm.rank
			}
		}
	}
	return limit
}

// masterRef is a sensor a problem is on.
type masterRef struct{ id, key string }

// judge applies one master to a problem: never to the master's own problems; held when the master is
// down; held for a while when the master hasn't reported since the problem began, or its ping (or
// collector) just failed (the "unreachable" trigger needs a few failed checks), so the device's real
// state decides.
func (m masterSet) judge(master masterItem, items []masterRef, start, now int64) holdVerdict {
	for _, it := range items {
		if it.id == master.itemID {
			return holdVerdict{}
		}
	}
	if m.down[master.itemID] {
		return holdVerdict{held: true, down: true}
	}
	if now-start < masterWaitSecs {
		if master.lastClock < start {
			return holdVerdict{held: true}
		}
		if isReachabilityKey(master.key) && strings.TrimSpace(master.lastValue) == "0" {
			return holdVerdict{held: true}
		}
	}
	return holdVerdict{}
}

// heldRef names the master a sensor waits on while that master is down.
type heldRef struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	ItemID   string `json:"item_id"`
	Name     string `json:"name"`
	Via      string `json:"via,omitempty"` // upstream: the master is the device this host is plugged into
}

// markHeld applies the notifier's rule to the census: an unacknowledged error or warning whose alerts
// a down master holds names that master (HeldBy), and the master's row counts it (Holds). The lists
// then show the master and fold the rest behind it, so the page and the alerts agree on what is one
// incident. Only a master that is down holds here, not one yet to report, so rows don't come and go
// while a device settles. Best effort: whatever can't be read leaves the rows as they are.
func (s *Server) markHeld(ctx context.Context, rows []sensorRow, problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) {
	if len(masterDown(problems, targets)) == 0 {
		return // nothing is down, so nothing is held
	}
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return
	}
	set := loadMasters(ctx, s.st, s.zbx, hosts, problems, targets)
	hostName := make(map[string]string, len(hosts))
	for _, h := range hosts {
		hostName[h.HostID] = h.Name
	}
	byItem := make(map[string]int, len(rows))
	for i := range rows {
		byItem[rows[i].ItemID] = i
	}
	now := time.Now().Unix()
	for i := range rows {
		r := &rows[i]
		if r.State != "error" && r.State != "warning" {
			continue
		}
		// Begun long ago as far as the hold goes: only a master that is down holds, not one yet to report.
		v := set.hold(r.HostID, []masterRef{{id: r.ItemID, key: r.key}}, 0, now)
		if !v.down {
			continue
		}
		ref := &heldRef{HostID: v.host, HostName: hostName[v.host], ItemID: v.master.itemID, Name: sensorLabel(v.master.key, v.master.key)}
		if v.by == "upstream" {
			ref.Via = "upstream"
		}
		if j, ok := byItem[v.master.itemID]; ok {
			if rows[j].Label != "" {
				ref.Name = rows[j].Label
			} else {
				ref.Name = rows[j].Name
			}
			rows[j].Holds++
		}
		r.HeldBy = ref
	}
}

// masterRefs lists the sensors a trigger is on.
func masterRefs(t zabbix.TriggerTarget) []masterRef {
	out := make([]masterRef, 0, len(t.Items))
	for _, it := range t.Items {
		out = append(out, masterRef{id: it.ItemID, key: it.Key})
	}
	return out
}

// masterConfig is the host-settings view of a host's master sensor. The options are the host's
// sensors that have an alert (a master only matters when it can go down), labelled like the tree.
func (s *Server) masterConfig(ctx context.Context, hostID string, items []zabbix.Item) *masterView {
	v := &masterView{Options: []masterOption{}}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ItemID)
		if it.Key == defaultMasterKey || it.Key == probeMasterKey {
			v.DefaultID = it.ItemID
		}
	}
	trigs, _ := s.zbx.ItemTriggers(ctx, ids)
	for _, it := range items {
		if len(trigs[it.ItemID]) == 0 {
			continue
		}
		v.Options = append(v.Options, masterOption{ID: it.ItemID, Label: sensorLabel(it.Key, it.Name)})
	}
	sort.SliceStable(v.Options, func(i, j int) bool { return naturalLess(v.Options[i].Label, v.Options[j].Label) })
	v.ItemID = v.DefaultID
	if id, ok, _ := s.st.HostMaster(ctx, hostID); ok {
		v.ItemID, v.Custom = id, true
	}
	return v
}

// applyMaster saves a host's master choice: "default" (the ping sensor), "none", or one of the host's
// own sensors.
func (s *Server) applyMaster(ctx context.Context, hostID, choice string) error {
	switch choice = strings.TrimSpace(choice); choice {
	case "", "default":
		return s.st.ClearHostMaster(ctx, hostID)
	case "none":
		return s.st.SetHostMaster(ctx, hostID, "")
	}
	items, err := s.zbx.ItemsByIDs(ctx, []string{choice})
	if err != nil {
		return err
	}
	if it, ok := items[choice]; !ok || it.HostID != hostID {
		return fmt.Errorf("the master sensor must be one of this host's sensors")
	}
	return s.st.SetHostMaster(ctx, hostID, choice)
}
