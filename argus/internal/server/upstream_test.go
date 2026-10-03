// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// The controller's answer: an AP names the switch it hangs off (its uplink MAC and port), a switch
// lists the wired clients on its ports (matched by IP, else by the MAC discovery saw), and an address
// two hosts share says nothing.
func TestControllerUpstreams(t *testing.T) {
	items := []zabbix.Item{
		{HostID: "1", Key: "unifi.mac", LastValue: "00:00:5e:00:53:01"},   // gateway
		{HostID: "2", Key: "unifi.mac", LastValue: "00:00:5E:00:53:10"},   // core switch
		{HostID: "2", Key: "unifi.uplink.mac", LastValue: "00005e005301"}, // plugged into the gateway
		{HostID: "2", Key: "unifi.uplink.port", LastValue: "8"},
		{HostID: "3", Key: "unifi.mac", LastValue: "00:00:5e:00:53:21"}, // floor switch
		{HostID: "3", Key: "unifi.uplink.mac", LastValue: "00:00:5e:00:53:10"},
		{HostID: "3", Key: "unifi.uplink.port", LastValue: "24"},
		{HostID: "4", Key: "unifi.uplink.mac", LastValue: "00:00:5e:00:53:21"}, // an AP on the floor switch
		{HostID: "4", Key: "unifi.uplink.port", LastValue: "3"},
		{HostID: "2", Key: "unifi.clients", LastValue: `[{"mac":"aa:bb:cc:00:00:01","ip":"10.0.0.9","port":5},{"mac":"aa:bb:cc:00:00:02","ip":"10.0.0.30","port":6},{"mac":"aa:bb:cc:00:00:03","ip":"10.0.0.50","port":7},{"mac":"00:00:5e:00:53:21","ip":"10.0.0.3","port":24}]`},
	}
	ips := map[string]string{"10": "10.0.0.9", "11": "10.0.0.88", "12": "10.0.0.50", "13": "10.0.0.50", "3": "10.0.0.3"}
	disc := map[string]string{"11": "AA-BB-CC-00-00-02"}
	got := controllerUpstreams(items, ips, disc)
	want := map[string]upstreamLink{
		"2":  {Host: "1", Port: "8", Source: "controller"},
		"3":  {Host: "2", Port: "24", Source: "controller"},
		"4":  {Host: "3", Port: "3", Source: "controller"},
		"10": {Host: "2", Port: "5", Source: "controller"}, // by its IP
		"11": {Host: "2", Port: "6", Source: "controller"}, // by the MAC discovery saw
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for h, w := range want {
		if got[h] != w {
			t.Errorf("host %s: got %+v want %+v", h, got[h], w)
		}
	}

	// A host set by hand, or to none, wins over the controller.
	eff := effectiveUpstreams(got, map[string]store.HostUpstream{"10": {Mode: "manual", ManualHost: "3"}, "11": {Mode: "none"}, "4": {Mode: "auto"}})
	if eff["10"].Host != "3" || eff["10"].Source != "manual" {
		t.Fatalf("manual: %+v", eff["10"])
	}
	if _, ok := eff["11"]; ok {
		t.Fatal("none kept an upstream")
	}
	if c := upstreamChain(eff, "4"); strings.Join(c, ",") != "3,2,1" {
		t.Fatalf("chain of 4: %v", c)
	}
	if b := behindHosts(eff, "2"); strings.Join(b, ",") != "10,3,4" {
		t.Fatalf("behind 2: %v", b)
	}
	// Two devices that report each other are no chain at all.
	loop := map[string]upstreamLink{"5": {Host: "6"}, "6": {Host: "5"}, "7": {Host: "5"}}
	if c := upstreamChain(loop, "5"); c != nil {
		t.Fatalf("a loop made a chain: %v", c)
	}
	if c := upstreamChain(loop, "7"); c != nil {
		t.Fatalf("a chain into a loop: %v", c)
	}
}

// A host behind a down switch is held, the switch's own alert isn't, and a loop never holds: a down
// device always gets its alert out (the alert-hold rule).
func TestUpstreamHold(t *testing.T) {
	m := masterSet{
		byHost: map[string][]masterItem{
			"1": {{itemID: "100", key: "icmpping", lastValue: "1", lastClock: 1000}}, // gateway
			"2": {{itemID: "200", key: "icmpping", lastValue: "1", lastClock: 1000}}, // switch
			"4": {{itemID: "400", key: "icmpping", lastValue: "1", lastClock: 1000}}, // AP
			"5": {{itemID: "500", key: "icmpping", lastValue: "1", lastClock: 1000}},
			"6": {{itemID: "600", key: "icmpping", lastValue: "1", lastClock: 1000}},
		},
		siteMaster: map[string]masterItem{}, probeHost: map[string]string{}, hostProxy: map[string]string{}, proxyByName: map[string]string{},
		down:     map[string]bool{"200": true},
		upstream: map[string]upstreamLink{"2": {Host: "1"}, "4": {Host: "2"}, "5": {Host: "6"}, "6": {Host: "5"}},
	}
	apPing := []masterRef{{id: "400", key: "icmpping"}}
	if v := m.hold("4", apPing, 990, 1100); !v.held || !v.down || v.by != "upstream" || v.host != "2" {
		t.Fatalf("AP behind a down switch: %+v", v)
	}
	if v := m.hold("2", []masterRef{{id: "200", key: "icmpping"}}, 990, 1100); v.held {
		t.Fatalf("the down switch's own alert was held: %+v", v)
	}
	// The switch is held only if what it's plugged into is down: the gateway is up, so no.
	m.down["100"] = true
	if v := m.hold("2", []masterRef{{id: "200", key: "icmpping"}}, 990, 1100); !v.held || v.host != "1" {
		t.Fatalf("switch behind a down gateway: %+v", v)
	}
	if v := m.hold("1", []masterRef{{id: "100", key: "icmpping"}}, 990, 1100); v.held {
		t.Fatalf("the top of the outage was held: %+v", v)
	}
	// Two hosts that name each other, both down: neither holds the other.
	m.down["500"], m.down["600"] = true, true
	if v := m.hold("5", []masterRef{{id: "500", key: "icmpping"}}, 990, 1100); v.held {
		t.Fatalf("a loop held an alert: %+v", v)
	}
	// The alert of a device with hosts behind it names them.
	names := map[string]string{"1": "gw-site1", "2": "sw-core", "4": "ap-lobby"}
	if b := m.behindFor("1", []masterRef{{id: "100"}}, names); b != "ap-lobby, sw-core" {
		t.Fatalf("behind the gateway: %q", b)
	}
	if b := m.behindFor("1", []masterRef{{id: "101"}}, names); b != "" {
		t.Fatalf("a problem on another sensor named the hosts behind: %q", b)
	}
	if b := behindText([]string{"e", "d", "c", "b", "a"}); b != "a, b, c and 2 more" {
		t.Fatalf("behindText: %q", b)
	}
}
