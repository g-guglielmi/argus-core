// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Master sensors (PRTG-style dependencies). Every host has a master sensor - its ICMP ping by default,
// or another sensor (or none) chosen in its settings. While the master is down, the host's other
// alerts are held: an unreachable device then sends one "unavailable" alert rather than one per
// sensor. On top of that, a probe's own reachability is the master of its whole site: while a probe
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

// proxyItemRe matches Zabbix's own per-proxy checks ("Zabbix server health" template), whose first key
// parameter is the proxy name: zabbix.proxy.last_seen[proxy-site1].
var proxyItemRe = regexp.MustCompile(`^zabbix\.proxy\.[a-z_.]+\["?([^",\]]+)"?`)

// masterItem is a master sensor's identity and latest reading.
type masterItem struct {
	itemID    string
	key       string
	lastValue string
	lastClock int64
}

// masterSet is everything the notifier needs to decide, per problem, whether a master holds it.
type masterSet struct {
	byHost      map[string]masterItem // host id -> its master sensor (absent = none)
	siteMaster  map[string]masterItem // proxy id -> its Probe host's reporting sensor
	probeHost   map[string]string     // proxy id -> its Probe host id
	hostProxy   map[string]string     // host id -> the proxy monitoring it ("" / "0" = the server)
	proxyByName map[string]string     // proxy name -> proxy id
	down        map[string]bool       // master item id -> it has an open "down" problem
}

// loadMasters builds the master set for one notifier tick. Best effort: whatever can't be read just
// means no hold for the hosts it concerns.
func loadMasters(ctx context.Context, st *store.Store, zbx *zabbix.Client, hosts []zabbix.Host, problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) masterSet {
	m := masterSet{
		byHost: map[string]masterItem{}, siteMaster: map[string]masterItem{}, probeHost: map[string]string{},
		hostProxy: map[string]string{}, proxyByName: map[string]string{}, down: masterDown(problems, targets),
	}
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

	// Default masters: each host's ping and each Probe host's reporting sensor, in one lookup.
	defaults := map[string]masterItem{}
	if items, err := zbx.ItemsByKeys(ctx, []string{defaultMasterKey, probeMasterKey}); err == nil {
		for _, it := range items {
			if it.Status != "0" || it.State == "1" {
				continue // a disabled or unsupported sensor never reports, so it can't vouch for the host
			}
			mi := masterItem{itemID: it.ItemID, key: it.Key, lastValue: it.LastValue, lastClock: atoi64(it.LastClock)}
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
			if it, found := customItems[id]; found && id != "" {
				m.byHost[h.HostID] = masterItem{itemID: it.ItemID, key: it.Key, lastValue: it.LastValue, lastClock: atoi64(it.LastClock)}
			}
			continue // an override (even "none") replaces the default
		}
		if mi, ok := defaults[h.HostID]; ok {
			m.byHost[h.HostID] = mi
		}
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
	by         string // which master: "site" or "host", for the log
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
			v.by = "site"
			return v
		}
	}
	if hm, ok := m.byHost[hostID]; ok {
		if v := m.judge(hm, items, start, now); v.held {
			v.by = "host"
			return v
		}
	}
	return holdVerdict{}
}

// masterRef is a sensor a problem is on.
type masterRef struct{ id, key string }

// judge applies one master to a problem: never to the master's own problems; held when the master is
// down; held for a while when the master hasn't reported since the problem began, or its ping just
// failed (the "unavailable" trigger needs a few failed checks), so the device's real state decides.
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
		if master.key == defaultMasterKey && strings.TrimSpace(master.lastValue) == "0" {
			return holdVerdict{held: true}
		}
	}
	return holdVerdict{}
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
