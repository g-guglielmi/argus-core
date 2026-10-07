// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/provision"
	"argus/internal/store"
)

// A site's map: the network devices always, the hosts linked to them, each link's traffic from the
// upstream's port (down = what the port sends), else from the device's own uplink, and the internet
// above the gateway.
func TestBuildSiteMap(t *testing.T) {
	const now = int64(1_000_000)
	fresh := now - 30
	row := func(host, key, val string, state string, item string) sensorRow {
		if state == "" {
			state = "ok"
		}
		return sensorRow{key: key, HostID: host, ItemID: item, Name: key, Value: val, LastClock: fresh, State: state}
	}
	named := func(r sensorRow, name string) sensorRow { r.Name = name; return r }
	stale := row("sw", "unifi.port.out[7]", "999", "", "i-stale")
	stale.LastClock = now - 3600
	rows := []sensorRow{
		// the gateway: WAN 1, and port 4 to the switch
		row("gw", "unifi.wan.in[1]", "412000000", "", "i-wan-in"),
		row("gw", "unifi.wan.out[1]", "38000000", "", "i-wan-out"),
		row("gw", "unifi.wan.latency[1]", "9", "warning", "i-wan-lat"),
		row("gw", "unifi.port.in[4]", "37000000", "", "i-gw4-in"),
		row("gw", "unifi.port.out[4]", "410000000", "", "i-gw4-out"),
		row("gw", "unifi.port.speed[4]", "1000000000", "", ""),
		row("gw", "unifi.port.state[4]", "1", "", ""),
		// the switch: port 2 to the AP (no link), port 9 to the NAS (busy), port 7 stale
		row("sw", "unifi.port.state[2]", "0", "", ""),
		row("sw", "unifi.port.in[2]", "0", "", "i-sw2-in"),
		row("sw", "unifi.port.out[2]", "0", "", ""),
		named(row("sw", "unifi.port.in[9]", "12000000", "", "i-sw9-in"), "Port 9 traffic in (NAS)"),
		row("sw", "unifi.port.out[9]", "950000000", "", ""),
		row("sw", "unifi.port.speed[9]", "1000000000", "", ""),
		stale,
		row("sw", "unifi.port.in[7]", "1", "", "i-sw7-in"),
		// the AP is down; the second AP hangs off the NAS by hand and reports its own uplink
		{key: defaultMasterKey, HostID: "ap", State: "error", Since: now - 600, LastClock: fresh, Value: "0"},
		row("ap", "unifi.clients", "14", "", ""),
		row("ap2", "unifi.uplink.in", "64000000", "", "i-ap2-up"),
		row("ap2", "unifi.uplink.out", "9000000", "", ""),
		row("nas", "disk.temp", "50", "error", ""),
		// a host of another probe
		row("other", "unifi.port.in[1]", "1", "", ""),
	}
	in := mapInput{
		probe: "proxy-site1", site: "site1", now: now, rows: rows,
		hosts: map[string]hostInfo{
			"gw": {Name: "gw-site1"}, "sw": {Name: "sw-core"}, "ap": {Name: "ap-lobby"}, "ap2": {Name: "ap-office"},
			"nas": {Name: "nas1"}, "web": {Name: "web1"}, "lone": {Name: "sw-spare"}, "probe": {Name: "argus-probe-site1"},
			"pc": {Name: "pc1"}, "tv": {Name: "tv1"},
		},
		classes: map[string]string{"gw": "unifi-gateway", "sw": "unifi-switch", "ap": "unifi-ap", "ap2": "unifi-ap", "lone": "unifi-switch", "probe": provision.ClassProbe},
		eff: map[string]upstreamLink{
			"sw":    {Host: "gw", Port: "4", Source: "controller"},
			"ap":    {Host: "sw", Port: "2", Source: "controller"},
			"nas":   {Host: "sw", Port: "9", Source: "controller"},
			"tv":    {Host: "sw", Port: "7", Source: "controller"},
			"ap2":   {Host: "nas", Source: "manual"},
			"pc":    {Host: "other", Port: "1", Source: "controller"}, // its upstream is on another site
			"probe": {Host: "sw", Port: "5", Source: "controller"},
		},
		facts: map[string]*deviceFacts{"sw": {Model: "USW Pro 24"}, "nas": {IP: "10.0.0.20"}},
		lines: []store.SiteLine{{Name: "WAN 1", HostID: "gw", Key: "unifi.wan.avail[1]", Provider: "Example Fiber", DownMbps: 1000, UpMbps: 50}},
	}
	v := buildSiteMap(in)

	nodes := map[string]mapNode{}
	for _, n := range v.Nodes {
		nodes[n.ID] = n
	}
	links := map[string]mapLink{}
	for _, l := range v.Links {
		links[l.To] = l
	}
	// Network devices always; hosts only when linked; Argus's probe host never.
	for _, id := range []string{"gw", "sw", "ap", "ap2", "nas", "lone", "tv", "wan:gw:1"} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("node %s missing: %+v", id, v.Nodes)
		}
	}
	if _, ok := nodes["probe"]; ok {
		t.Error("the probe host is not a device on the map")
	}
	if len(v.Unplaced) != 2 || v.Unplaced[0].Name != "pc1" || v.Unplaced[1].Name != "web1" {
		t.Errorf("unplaced: %+v", v.Unplaced)
	}
	if n := nodes["sw"]; n.Kind != "switch" || n.Model != "USW Pro 24" || n.State != "ok" {
		t.Errorf("switch: %+v", n)
	}
	if n := nodes["ap"]; n.State != "down" || n.DownSince != now-600 || n.Clients == nil || *n.Clients != 14 {
		t.Errorf("down AP: %+v", n)
	}
	if n := nodes["nas"]; n.Kind != "host" || n.State != "error" || n.Errors != 1 || n.IP != "10.0.0.20" {
		t.Errorf("NAS: %+v", n)
	}

	if l := links["sw"]; l.From != "gw" || *l.Down != 410e6 || *l.Up != 37e6 || l.Use == nil || *l.Use != 41 || l.ChartItem != "i-gw4-in" || l.ChartHost != "gw" {
		t.Errorf("gateway to switch: %+v", l)
	}
	if l := links["ap"]; !l.NoLink {
		t.Errorf("no link to the AP: %+v", l)
	}
	if l := links["nas"]; l.Use == nil || *l.Use != 95 || l.PortName != "NAS" {
		t.Errorf("busy NAS link: %+v", l)
	}
	if l := links["tv"]; l.Down != nil || l.Up == nil || l.Use != nil {
		t.Errorf("a stale reading is unknown: %+v", l)
	}
	if l := links["ap2"]; l.Source != "manual" || l.Down == nil || *l.Down != 64e6 || l.ChartHost != "ap2" || l.ChartItem != "i-ap2-up" || l.Use != nil {
		t.Errorf("uplink fallback: %+v", l)
	}
	// The line's upload is slower: 38 of 50 Mbps up is fuller than 412 of 1000 down.
	if l := links["gw"]; l.From != "wan:gw:1" || *l.Down != 412e6 || *l.Latency != 9 || l.State != "warning" || l.ChartItem != "i-wan-in" ||
		l.Speed != 1e9 || l.SpeedUp != 50e6 || l.Use == nil || *l.Use != 76 {
		t.Errorf("internet: %+v", l)
	}
	if n := nodes["wan:gw:1"]; n.Kind != "internet" || n.Provider != "Example Fiber" {
		t.Errorf("internet node: %+v", n)
	}

	var sv mapSiteView
	sv.summarize(v)
	if sv.Devices != 5 || sv.Hosts != 2 || sv.Down != 1 || sv.Busy != 2 || sv.NoLink != 1 || sv.Busiest == nil || sv.Busiest.To != "nas1" || sv.Busiest.Use != 95 {
		t.Errorf("summary: %+v %+v", sv, sv.Busiest)
	}
}

// A gateway's WAN takes the line tied to that WAN; a line tied to the whole gateway only when it has
// one WAN. The busiest link, when it is the internet, is named by the gateway's WAN.
func TestWanLine(t *testing.T) {
	lines := []store.SiteLine{
		{Name: "WAN 2", HostID: "gw", Key: "unifi.wan.latency[2]", DownMbps: 300},
		{Name: "Modem", HostID: "gw", DownMbps: 1000},
	}
	if l, ok := wanLine(lines, "gw", "2", 2); !ok || l.DownMbps != 300 {
		t.Fatalf("WAN 2: %+v %v", l, ok)
	}
	if _, ok := wanLine(lines, "gw", "1", 2); ok {
		t.Fatal("a whole-gateway line can't say which of two WANs it is")
	}
	if l, ok := wanLine(lines, "gw", "1", 1); !ok || l.Name != "Modem" {
		t.Fatalf("the only WAN: %+v %v", l, ok)
	}
	use := 88
	var sv mapSiteView
	sv.summarize(mapView{
		Nodes: []mapNode{{ID: "wan:gw:1", Name: "Internet", Kind: "internet"}, {ID: "gw", Name: "gw-site1", Kind: "gateway"}},
		Links: []mapLink{{From: "wan:gw:1", To: "gw", Port: "1", Use: &use}},
	})
	if b := sv.Busiest; b == nil || b.From != "gw-site1" || b.Wan != "1" || b.To != "" || sv.Busy != 1 {
		t.Fatalf("busiest internet line: %+v", b)
	}
}

func TestLineSpeed(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want float64
		ok   bool
	}{{0, 0, true}, {1000, 1000, true}, {12.345, 12.35, true}, {-1, 0, false}, {2e6, 0, false}} {
		if got, ok := lineMbps(c.in); ok != c.ok || got != c.want {
			t.Errorf("lineMbps(%v) = %v %v", c.in, got, ok)
		}
	}
	if s := lineSpeed(store.SiteLine{DownMbps: 1000, UpMbps: 300}); s != "1000/300 Mbps" {
		t.Errorf("asymmetric: %q", s)
	}
	if s := lineSpeed(store.SiteLine{DownMbps: 1000, UpMbps: 1000}); s != "1000 Mbps" {
		t.Errorf("symmetric: %q", s)
	}
	if s := lineSpeed(store.SiteLine{UpMbps: 100}); s != "" {
		t.Errorf("no download, no speed: %q", s)
	}
}

func TestCleanPins(t *testing.T) {
	got, ok := cleanPins(map[string]store.MapPin{"10": {DX: 40.4, DY: -12.6}, "11": {}})
	if !ok || len(got) != 1 || got["10"].DX != 40 || got["10"].DY != -13 {
		t.Fatalf("rounded, unmoved dropped: %+v %v", got, ok)
	}
	if _, ok := cleanPins(map[string]store.MapPin{"10": {DX: 25000}}); ok {
		t.Fatal("too far")
	}
	if _, ok := cleanPins(map[string]store.MapPin{"": {DX: 1}}); ok {
		t.Fatal("no id")
	}
}
