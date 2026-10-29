// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"

	"argus/internal/zabbix"
)

func TestSyntheticHelpers(t *testing.T) {
	if !isSynthetic(synthUnsupported+"42") || !isSynthetic(synthInterface+"7") || isSynthetic("123456") {
		t.Fatal("isSynthetic misclassified an id")
	}
	for typ, want := range map[string]string{"1": "Zabbix agent not reachable", "2": "SNMP not responding", "9": "Monitoring interface not reachable"} {
		if got := interfaceDownName(typ); got != want {
			t.Errorf("interfaceDownName(%s) = %q", typ, got)
		}
	}
	if got := unsupportedReading("Timeout while executing a shell script.\nmore detail"); got != "Not supported: Timeout while executing a shell script." {
		t.Errorf("reading: %q", got)
	}
	if got := unsupportedReading(""); got != "Not supported" {
		t.Errorf("empty reading: %q", got)
	}
	if got := unsupportedReading(strings.Repeat("x", 300)); len([]rune(got)) > 230 {
		t.Errorf("long reading not capped: %d", len(got))
	}
}

// A master in the unsupported list is failing: its dependents say nothing of their own, and it
// stands for them when they have collected (its own clock never says: a raw master keeps no history).
func TestFailingMasters(t *testing.T) {
	items := []zabbix.UnsupportedItem{
		{ItemID: "10", Key: "unifi.switch.raw", LastClock: "0"},
		{ItemID: "11", Key: "unifi.cpu.util", MasterItemID: "10", LastClock: "1000"},
		{ItemID: "12", Key: "unifi.mem.util", MasterItemID: "10", LastClock: "1000"},
		{ItemID: "20", Key: "hass.raw", LastClock: "0"},
		{ItemID: "21", Key: "hass.running", MasterItemID: "20", LastClock: "0"},    // never collected: a new host
		{ItemID: "31", Key: "snmp.if.in[3]", MasterItemID: "30", LastClock: "900"}, // its master is fine
	}
	failing, collected := failingMasters(items)
	if !failing["10"] || !failing["20"] || failing["30"] || !failing["11"] {
		t.Fatalf("failing: %v", failing)
	}
	if !collected["10"] || collected["20"] || collected["30"] || collected["11"] {
		t.Fatalf("collected: %v", collected)
	}
}

// Argus-raised problems join Zabbix's list and target map without disturbing them.
func TestSyntheticMerge(t *testing.T) {
	id := synthUnsupported + "900"
	s := synthSet{
		problems: []zabbix.Problem{{EventID: id, ObjectID: id, Severity: "2"}},
		targets:  map[string]zabbix.TriggerTarget{id: {Hosts: []zabbix.TargetHost{{HostID: "50"}}, Items: []zabbix.TargetItem{{ItemID: "900"}}}},
	}
	ps, ts := s.merge([]zabbix.Problem{{EventID: "1", ObjectID: "t1"}}, map[string]zabbix.TriggerTarget{"t1": {}})
	if len(ps) != 2 || ps[1].EventID != id || len(ts) != 2 || ts[id].Hosts[0].HostID != "50" {
		t.Fatalf("merge: %+v %+v", ps, ts)
	}
	if _, ts2 := s.merge(nil, nil); len(ts2) != 1 {
		t.Fatalf("merge into nil targets: %+v", ts2)
	}
}

func TestIntervalSecs(t *testing.T) {
	cases := map[string]int64{
		"30s": 30, "1m": 60, "5m": 300, "1h": 3600, "1d": 86400, "90": 90,
		"1m;50s/1-5,09:00-18:00": 60, "0": 60, "{$NUT.INTERVAL}": 60, "": 60,
	}
	for in, want := range cases {
		if got := intervalSecs(in); got != want {
			t.Errorf("intervalSecs(%q) = %d, want %d", in, got, want)
		}
	}
}
