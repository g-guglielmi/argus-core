// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"testing"

	"argus/internal/zabbix"
)

// A site with one probe (proxy 7, Probe host 70 with reporting sensor 700) monitoring host 10 (ping
// sensor 100, CPU sensor 101). Host 20 is the Zabbix server, with Zabbix's per-proxy check 200.
func testMasters(down ...string) masterSet {
	m := masterSet{
		byHost: map[string][]masterItem{
			"10": {{itemID: "100", key: "icmpping", lastValue: "1", lastClock: 1000}},
			"70": {{itemID: "700", key: "zabbix[uptime]", lastClock: 1000}},
			// A UPS read through NUT: ping plus the collector's reachability sensor.
			"30": {{itemID: "300", key: "icmpping", lastValue: "1", lastClock: 1000}, {itemID: "301", key: "nut.reachable", lastValue: "1", lastClock: 1000, rank: 1}},
			// A Linux host read over SSH that also runs NUT: two collectors, one rank.
			"40": {
				{itemID: "400", key: "icmpping", lastValue: "1", lastClock: 1000},
				{itemID: "401", key: "linux.ssh.reachable", lastValue: "1", lastClock: 1000, rank: 1},
				{itemID: "402", key: "nut.reachable", lastValue: "1", lastClock: 1000, rank: 1},
			},
		},
		siteMaster:  map[string]masterItem{"7": {itemID: "700", key: "zabbix[uptime]", lastClock: 1000}},
		probeHost:   map[string]string{"7": "70"},
		hostProxy:   map[string]string{"10": "7", "70": "7", "20": "0", "30": "0", "40": "0"},
		proxyByName: map[string]string{"proxy-site1": "7"},
		down:        map[string]bool{},
	}
	for _, d := range down {
		m.down[d] = true
	}
	return m
}

func TestMasterHold(t *testing.T) {
	cpu := []masterRef{{id: "101", key: "system.cpu.util"}}
	ping := []masterRef{{id: "100", key: "icmpping"}}

	// Master up and reporting since the problem began: nothing is held.
	if v := testMasters().hold("10", cpu, 990, 1100); v.held {
		t.Fatalf("healthy master held an alert: %+v", v)
	}
	// Ping down: the CPU alert is held (down, so it waits out the delay after recovery)...
	if v := testMasters("100").hold("10", cpu, 990, 1100); !v.held || !v.down || v.by != "host" {
		t.Fatalf("down ping: %+v", v)
	}
	// ...but the ping alert itself goes out.
	if v := testMasters("100").hold("10", ping, 990, 1100); v.held {
		t.Fatalf("the master's own alert was held: %+v", v)
	}
	// Probe not reporting: everything on its site is held, ping included.
	if v := testMasters("700").hold("10", ping, 990, 1100); !v.held || v.by != "site" {
		t.Fatalf("site hold: %+v", v)
	}
	// ...but not the Probe host's own alerts.
	if v := testMasters("700").hold("70", []masterRef{{id: "700", key: "zabbix[uptime]"}}, 990, 1100); v.held {
		t.Fatalf("the Probe host held itself: %+v", v)
	}
	// Zabbix's own per-proxy check on the Zabbix server host follows that proxy's probe.
	last := []masterRef{{id: "200", key: "zabbix.proxy.last_seen[proxy-site1]"}}
	if v := testMasters("700").hold("20", last, 990, 1100); !v.held || v.by != "site" {
		t.Fatalf("per-proxy check: %+v", v)
	}
	if v := testMasters().hold("20", last, 990, 1100); v.held {
		t.Fatalf("per-proxy check held with the probe up: %+v", v)
	}

	// A problem that began after the master last reported waits for it (not "down": no extra delay
	// once it reports), but only for a while.
	if v := testMasters().hold("10", cpu, 1010, 1100); !v.held || v.down {
		t.Fatalf("waiting for the master: %+v", v)
	}
	if v := testMasters().hold("10", cpu, 1010, 1010+masterWaitSecs); v.held {
		t.Fatalf("waited past the limit: %+v", v)
	}
	// A failed ping check that hasn't tripped the trigger yet holds too.
	m := testMasters()
	m.byHost["10"] = []masterItem{{itemID: "100", key: "icmpping", lastValue: "0", lastClock: 1050}}
	if v := m.hold("10", cpu, 1010, 1100); !v.held {
		t.Fatalf("failing ping: %+v", v)
	}

	// NUT server stopped, machine still up: the collector holds the UPS readings, and its own
	// "unreachable" alert goes out.
	battery := []masterRef{{id: "302", key: "nut.battery.charge"}}
	nutUnreach := []masterRef{{id: "301", key: "nut.reachable"}}
	if v := testMasters("301").hold("30", battery, 990, 1100); !v.held || !v.down {
		t.Fatalf("collector down: %+v", v)
	}
	if v := testMasters("301").hold("30", nutUnreach, 990, 1100); v.held {
		t.Fatalf("the collector's own alert was held: %+v", v)
	}
	// The machine down: ping holds the collector's alert too...
	if v := testMasters("300", "301").hold("30", nutUnreach, 990, 1100); !v.held {
		t.Fatalf("ping down must hold the collector alert: %+v", v)
	}
	// ...and its own "unavailable" alert goes out: the down collector must not hold it back, or the
	// two would hold each other and nothing would be sent.
	upsPing := []masterRef{{id: "300", key: "icmpping"}}
	if v := testMasters("300", "301").hold("30", upsPing, 990, 1100); v.held {
		t.Fatalf("a down collector held the ping alert: %+v", v)
	}
	if v := testMasters("300", "301").hold("30", battery, 990, 1100); !v.held {
		t.Fatalf("the readings must stay held: %+v", v)
	}
	// Nor does a collector that just failed hold the ping alert while it waits.
	m = testMasters("300")
	m.byHost["30"][1].lastValue, m.byHost["30"][1].lastClock = "0", 1050
	if v := m.hold("30", upsPing, 1010, 1100); v.held {
		t.Fatalf("a failing collector held the ping alert: %+v", v)
	}
	// Two collectors on one host, both down, the machine up: each alerts on its own.
	for _, c := range []masterRef{{id: "401", key: "linux.ssh.reachable"}, {id: "402", key: "nut.reachable"}} {
		if v := testMasters("401", "402").hold("40", []masterRef{c}, 990, 1100); v.held {
			t.Fatalf("collector %s held by its peer: %+v", c.key, v)
		}
	}
	// A collector chosen as the host's main master ranks first, so the ping (now just a sensor) is
	// held by it and it by nothing.
	m = testMasters("301", "300")
	m.byHost["30"] = []masterItem{{itemID: "301", key: "nut.reachable", lastClock: 1000}, {itemID: "301", key: "nut.reachable", lastClock: 1000, rank: 1}}
	if v := m.hold("30", nutUnreach, 990, 1100); v.held {
		t.Fatalf("the chosen master was held: %+v", v)
	}
	if v := m.hold("30", upsPing, 990, 1100); !v.held {
		t.Fatalf("the chosen master must hold the ping: %+v", v)
	}
	// A fresh 0 from the collector holds before its trigger fires.
	m = testMasters()
	m.byHost["30"][1].lastValue, m.byHost["30"][1].lastClock = "0", 1050
	if v := m.hold("30", battery, 1010, 1100); !v.held {
		t.Fatalf("failing collector: %+v", v)
	}
}

// After a master recovers, what it holds waits out the settle window: a service still starting or
// an agent not yet listening on a host coming back up doesn't alert on the way, whatever the problem
// began; past the window, or with the window off, nothing is held. The master's own problems never
// are, and a window only counts for a master that is back (one still down holds as down).
func TestSettleHold(t *testing.T) {
	cpu := []masterRef{{id: "101", key: "system.cpu.util"}}
	ping := []masterRef{{id: "100", key: "icmpping"}}
	// settled is the set with one master (the host's ping, or the site's probe) back at `at`, every
	// master reporting since (at 1090), so only the settle window can hold.
	settled := func(master string, at int64, settle int64, down ...string) masterSet {
		m := testMasters(down...)
		for h, ms := range m.byHost {
			for i := range ms {
				ms[i].lastClock = 1090
			}
			m.byHost[h] = ms
		}
		for k, sm := range m.siteMaster {
			sm.lastClock = 1090
			m.siteMaster[k] = sm
		}
		m.recovered = map[string]int64{master: at}
		m.settle = settle
		return m
	}
	// The ping came back 60 s ago, the window is 300 s: the CPU problem waits, not as "down".
	if v := settled("100", 1040, 300).hold("10", cpu, 1050, 1100); !v.held || v.down || !v.settling || v.by != "host" {
		t.Fatalf("inside the window: %+v", v)
	}
	// A problem that began before the recovery waits too.
	if v := settled("100", 1040, 300).hold("10", cpu, 900, 1100); !v.held || !v.settling {
		t.Fatalf("older problem inside the window: %+v", v)
	}
	// The ping's own problem goes out.
	if v := settled("100", 1040, 300).hold("10", ping, 1050, 1100); v.held {
		t.Fatalf("the master's own alert was held: %+v", v)
	}
	// Past the window: alerts as usual.
	if v := settled("100", 700, 300).hold("10", cpu, 900, 1100); v.held {
		t.Fatalf("past the window: %+v", v)
	}
	// Window off: no settling hold.
	if v := settled("100", 1040, 0).hold("10", cpu, 900, 1100); v.held {
		t.Fatalf("window off: %+v", v)
	}
	// The probe came back: its whole site waits, the ping problem of a host included.
	if v := settled("700", 1040, 300).hold("10", ping, 1050, 1100); !v.held || !v.settling || v.by != "site" {
		t.Fatalf("site settling: %+v", v)
	}
	// A master still down holds as down, whatever its last recovery says.
	if v := settled("100", 1040, 300, "100").hold("10", cpu, 1050, 1100); !v.held || !v.down || v.settling {
		t.Fatalf("down master: %+v", v)
	}
}

// A master's recovery time is its "down" trigger's last change while it is OK: an error-level or a
// "no data" trigger, enabled; a warning (packet loss), a trigger in problem, a disabled one or one
// that never fired don't count.
func TestMasterRecoveries(t *testing.T) {
	trigs := map[string][]zabbix.ItemTrigger{
		"100": {
			{TriggerID: "1", Priority: 4, Value: 0, LastChange: 5000},                              // unreachable, back at 5000
			{TriggerID: "2", Priority: 2, Value: 0, LastChange: 6000},                              // packet loss: a warning, not a "down"
			{TriggerID: "3", Priority: 4, Value: 0, LastChange: 4000},                              // an older down trigger
			{TriggerID: "4", Priority: 4, Value: 0, LastChange: 7000, Status: 1},                   // disabled
			{TriggerID: "5", Priority: 1, Value: 0, LastChange: 5500, Expression: "nodata(x,10m)"}, // no data, any severity
		},
		"200": {{TriggerID: "6", Priority: 4, Value: 1, LastChange: 5000}}, // in problem now
		"300": {{TriggerID: "7", Priority: 4, Value: 0, LastChange: 0}},    // never fired
	}
	got := masterRecoveries(trigs)
	if got["100"] != 5500 {
		t.Errorf("item 100: want 5500 (the newest down trigger back to OK), got %d", got["100"])
	}
	if _, has := got["200"]; has {
		t.Errorf("item 200 is in problem, must have no recovery")
	}
	if _, has := got["300"]; has {
		t.Errorf("item 300 never fired, must have no recovery")
	}
}

func TestMasterDown(t *testing.T) {
	var targets map[string]zabbix.TriggerTarget
	_ = json.Unmarshal([]byte(`{
		"1": {"expression": "max(/h/icmpping,#3)=0", "items": [{"itemid": "100"}]},
		"2": {"expression": "min(/h/icmppingloss,#3)>=20", "items": [{"itemid": "102"}]},
		"3": {"expression": "nodata(/p/zabbix[uptime],180,\"strict\")=1", "items": [{"itemid": "700"}]}
	}`), &targets)
	down := masterDown([]zabbix.Problem{
		{EventID: "a", ObjectID: "1", Severity: "4"},
		{EventID: "b", ObjectID: "2", Severity: "2"},
		{EventID: "c", ObjectID: "3", Severity: "2"},
	}, targets)
	if !down["100"] || down["102"] || !down["700"] {
		t.Fatalf("down = %v (want the error and the no-data warning, not the loss warning)", down)
	}
}

// The notifier notes, by its own clock, when a master it saw down is back; a master down again is
// forgotten until it is back again, and a recovery is forgotten after a day.
func TestNoteMastersDown(t *testing.T) {
	masterClock.Lock()
	masterClock.down, masterClock.back = map[string]bool{}, map[string]int64{}
	masterClock.Unlock()
	noteMastersDown(map[string]bool{"700": true}, 1000)
	if b := mastersBack(); len(b) != 0 {
		t.Fatalf("down: want nothing back, got %v", b)
	}
	noteMastersDown(map[string]bool{}, 1060)
	if b := mastersBack(); b["700"] != 1060 {
		t.Fatalf("back: want 700 at 1060, got %v", b)
	}
	noteMastersDown(map[string]bool{"700": true}, 1100)
	if b := mastersBack(); len(b) != 0 {
		t.Fatalf("down again: want nothing back, got %v", b)
	}
	noteMastersDown(map[string]bool{}, 1200)
	noteMastersDown(map[string]bool{}, 1200+masterBackKeep+1)
	if b := mastersBack(); len(b) != 0 {
		t.Fatalf("a day later: want it forgotten, got %v", b)
	}
}
