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
// lists the wired clients on its ports (matched by IP, else by the MAC discovery saw), and hosts that
// share an address (a NAS and the services on it) are all behind the port it is listed on.
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
		"12": {Host: "2", Port: "7", Source: "controller"}, // a NAS and
		"13": {Host: "2", Port: "7", Source: "controller"}, // a service on it, at the same address
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
	if b := behindHosts(eff, "2"); strings.Join(b, ",") != "10,12,13,3,4" {
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

// No upstream says why, naming names: a UniFi switch without a list (its template not updated), a read
// that failed (with its reason), lists not read yet, or the address in none of them.
func TestUpstreamWhy(t *testing.T) {
	names := map[string]string{"2": "sw-core", "3": "sw-floor2", "4": "gw-site1"}
	all := []string{"2", "3", "4"}
	cases := []struct {
		items []zabbix.Item
		hosts []string
		want  []string
	}{
		{nil, nil, []string{"No UniFi switch or gateway lists its wired clients"}},
		{[]zabbix.Item{{HostID: "4", Key: "unifi.clients", LastClock: "100"}}, all,
			[]string{"sw-core, sw-floor2 have no wired-client list: their template hasn't been updated", "The wired clients of gw-site1 don't include 10.0.0.30"}},
		{[]zabbix.Item{{HostID: "2", Key: "unifi.clients", State: "1", Error: "UniFi API HTTP 401 reading the client list"}, {HostID: "3", Key: "unifi.clients", LastClock: "100"}}, []string{"2", "3"},
			[]string{"Reading the wired clients failed on sw-core: UniFi API HTTP 401", "The wired clients of sw-floor2 don't include"}},
		{[]zabbix.Item{{HostID: "2", Key: "unifi.clients", LastClock: "0"}, {HostID: "3", Key: "unifi.clients", LastClock: "100"}}, []string{"2", "3"},
			[]string{"sw-core hasn't read its wired clients yet"}},
	}
	for i, c := range cases {
		got := upstreamWhy(c.items, names, "10.0.0.30", c.hosts)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("case %d: %q lacks %q", i, got, w)
			}
		}
	}
}

// A new answer from the controller is taken only once it has held for upstreamSettle; one that goes
// back (or on to a third) before that is a flip, and the recorded answer stays.
func TestUpstreamStep(t *testing.T) {
	flex := upstreamLink{Host: "201", Port: "5", Source: "controller"}
	mini := upstreamLink{Host: "204", Port: "5", Source: "controller"}
	was := store.HostUpstream{AutoHost: "201", AutoPort: "5", AutoAt: 1000}

	// The first answer for a host is taken at once.
	if take, next, _ := upstreamStep(store.HostUpstream{}, flex, nil, 2000); !take || next != nil {
		t.Fatalf("first answer: take %v next %+v", take, next)
	}
	// The same answer again: nothing to do.
	if take, next, flipped := upstreamStep(was, flex, nil, 2000); take || next != nil || flipped {
		t.Fatalf("same answer: take %v next %+v flipped %v", take, next, flipped)
	}
	// A new answer waits...
	take, next, _ := upstreamStep(was, mini, nil, 2000)
	if take || next == nil || next.since != 2000 {
		t.Fatalf("new answer should wait: take %v next %+v", take, next)
	}
	if take, again, _ := upstreamStep(was, mini, next, 2000+upstreamSettle-1); take || again != next {
		t.Fatalf("taken before it held: take %v", take)
	}
	// ...and is taken once it has held.
	if take, _, _ := upstreamStep(was, mini, next, 2000+upstreamSettle); !take {
		t.Fatal("not taken after it held")
	}
	// Going back before it held is a flip, and the recorded answer stays.
	if take, cleared, flipped := upstreamStep(was, flex, next, 2300); take || cleared != nil || !flipped {
		t.Fatalf("flip back: take %v next %+v flipped %v", take, cleared, flipped)
	}
	// Moving on to a third answer is a flip too, and the wait starts over.
	third := upstreamLink{Host: "205", Port: "3", Source: "controller"}
	if take, n3, flipped := upstreamStep(was, third, next, 2300); take || !flipped || n3 == nil || n3.since != 2300 || n3.link != third {
		t.Fatalf("third answer: take %v next %+v flipped %v", take, n3, flipped)
	}
	// Losing the answer altogether waits like any other change.
	if take, _, _ := upstreamStep(was, upstreamLink{}, nil, 2000); take {
		t.Fatal("an empty answer was taken at once")
	}
}

// A host that flips upstreamFlapAfter times within the window is logged once, then not again for a day.
func TestUpstreamFlaps(t *testing.T) {
	var tr upstreamTrack
	for i, at := range []int64{0, 600, 1200} {
		n, log := tr.noteFlip("205", 10000+at)
		if n != i+1 || log != (i == 2) {
			t.Fatalf("flip %d: n %d log %v", i+1, n, log)
		}
	}
	if _, log := tr.noteFlip("205", 12000); log {
		t.Fatal("logged twice in a day")
	}
	// Flips older than the window drop out of the count.
	if n, _ := tr.noteFlip("205", 10000+upstreamFlapWindow+1300); n != 2 {
		t.Fatalf("old flips still counted: %d", n)
	}
	if _, log := tr.noteFlip("205", 10000+upstreamFlapRelog+1200); log {
		t.Fatal("a lone flip a day later shouldn't log")
	}
}

// The upstream in effect is the answer Argus took, not the controller's latest flip; a host with none
// taken yet uses the live answer.
func TestSettledUpstreams(t *testing.T) {
	live := map[string]upstreamLink{
		"205": {Host: "204", Port: "5", Source: "controller"}, // the controller's latest flip
		"300": {Host: "201", Port: "2", Source: "controller"}, // new: nothing taken yet
	}
	settings := map[string]store.HostUpstream{
		"205": {Mode: "auto", AutoHost: "201", AutoPort: "5", AutoAt: 1000},
		"206": {Mode: "auto", AutoHost: "201", AutoPort: "4", AutoAt: 1000}, // gone from the controller for now
	}
	got := settledUpstreams(live, settings)
	if got["205"].Host != "201" || got["205"].Port != "5" {
		t.Fatalf("205 should keep the answer Argus took: %+v", got["205"])
	}
	if got["300"].Host != "201" {
		t.Fatalf("a new host takes the live answer: %+v", got["300"])
	}
	if got["206"].Host != "201" {
		t.Fatalf("an answer the controller dropped for now is kept until the drop holds: %+v", got["206"])
	}
}

// The controller's flips for two USW Flex Minis (204, 205) on a USW Flex (201, uplink on port 1):
// each Mini placed on a port of the other with no link, or on the other's own uplink port. Both kinds
// are doubted; the true answers (201 port 3 and port 5, both linked) pass.
func TestDoubtUpstreams(t *testing.T) {
	own := map[string]string{"201": "1", "204": "1", "205": "1"}
	ports := map[string]map[string]string{
		"201": {"3": "1", "5": "1"},
		"204": {"1": "1", "5": "0"},
		"205": {"1": "1", "3": "0"},
	}
	cases := []struct {
		host string
		link upstreamLink
		want string
	}{
		{"204", upstreamLink{Host: "201", Port: "3"}, ""},
		{"205", upstreamLink{Host: "201", Port: "5"}, ""},
		{"205", upstreamLink{Host: "204", Port: "5"}, "that port has no link"},
		{"204", upstreamLink{Host: "205", Port: "3"}, "that port has no link"},
		{"205", upstreamLink{Host: "204", Port: "1"}, "that is the port it uses for its own uplink"},
		{"204", upstreamLink{Host: "205", Port: "1"}, "that is the port it uses for its own uplink"},
		{"251", upstreamLink{Host: "201", Port: ""}, ""},  // a wireless uplink names no port
		{"300", upstreamLink{Host: "999", Port: "7"}, ""}, // a port Argus knows nothing about
	}
	for _, c := range cases {
		links := map[string]upstreamLink{c.host: c.link}
		doubtUpstreams(links, own, ports)
		if got := links[c.host].Doubt; got != c.want {
			t.Errorf("%s on %s port %s: doubt %q, want %q", c.host, c.link.Host, c.link.Port, got, c.want)
		}
	}

	// A doubted answer is never taken as a new host's first answer either.
	live := map[string]upstreamLink{"205": {Host: "204", Port: "5", Doubt: "that port has no link"}}
	if got := settledUpstreams(live, map[string]store.HostUpstream{}); len(got) != 0 {
		t.Fatalf("a doubted first answer was taken: %+v", got)
	}
}
