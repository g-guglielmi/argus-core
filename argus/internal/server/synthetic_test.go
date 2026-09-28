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
