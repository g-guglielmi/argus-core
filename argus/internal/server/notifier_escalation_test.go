// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"testing"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestSensorStillAlerting(t *testing.T) {
	// Triggers: 1 = the probe's "not reporting" warning, 2 = its "unreachable" high (same item 900),
	// 3 = a different sensor (item 901) on the same host.
	var targets map[string]zabbix.TriggerTarget
	raw := `{
		"1": {"hosts": [{"hostid": "50"}], "items": [{"itemid": "900"}]},
		"2": {"hosts": [{"hostid": "50"}], "items": [{"itemid": "900"}]},
		"3": {"hosts": [{"hostid": "50"}], "items": [{"itemid": "901"}]}
	}`
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		t.Fatal(err)
	}
	warn := store.NotifyState{EventID: "e1", HostID: "50", ItemID: "900", Severity: 2, State: "firing"}
	high := store.NotifyState{EventID: "e2", HostID: "50", ItemID: "900", Severity: 4, State: "firing"}
	highOpen := []zabbix.Problem{{EventID: "e2", ObjectID: "2", Severity: "4"}}
	warnOpen := []zabbix.Problem{{EventID: "e1b", ObjectID: "1", Severity: "2"}}
	otherOpen := []zabbix.Problem{{EventID: "e3", ObjectID: "3", Severity: "4"}}

	if !sensorStillAlerting(warn, highOpen, targets) {
		t.Error("warning -> high on the same sensor must not read as a recovery")
	}
	if !sensorStillAlerting(high, warnOpen, targets) {
		t.Error("high -> warning on the same sensor must not read as a recovery")
	}
	if sensorStillAlerting(warn, nil, targets) {
		t.Error("nothing left open on the sensor: that is a real recovery")
	}
	if sensorStillAlerting(warn, otherOpen, targets) {
		t.Error("a problem on another sensor of the same host must not hold back this recovery")
	}
	if sensorStillAlerting(store.NotifyState{EventID: "e9", HostID: "50", Severity: 2}, highOpen, targets) {
		t.Error("without a recorded item there is nothing to correlate")
	}
}

func TestNoDataSince(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 9, 28, 1, 0, 0, 0, loc)
	if got := noDataSince(time.Date(2026, 9, 28, 0, 56, 0, 0, loc).Unix(), now); got != "No data for 4m (since 00:56)" {
		t.Errorf("same day: %q", got)
	}
	if got := noDataSince(time.Date(2026, 9, 27, 23, 10, 0, 0, loc).Unix(), now); got != "No data for 1h 50m (since Sep 27 23:10)" {
		t.Errorf("earlier day: %q", got)
	}
	if got := noDataSince(0, now); got != "No data received yet" {
		t.Errorf("never: %q", got)
	}
}

// TriggerTargets now asks for expanded expressions (real macro values, function names). The
// threshold shown in an alert must come from the comparison, not from digits inside the item key.
func TestParseThresholdExpanded(t *testing.T) {
	cases := map[string]string{
		`min(/Probe site1/zabbix[queue,10m],15m)>=50 and min(/Probe site1/zabbix[queue,10m],15m)<200`: ">=50",
		`min(/web1/system.cpu.util,5m)>90`:                   ">90",
		`nodata(/Probe site1/zabbix[uptime],300,"strict")=1`: "",
	}
	for expr, want := range cases {
		if got := parseThreshold(expr); got != want {
			t.Errorf("parseThreshold(%q) = %q, want %q", expr, got, want)
		}
	}
}
