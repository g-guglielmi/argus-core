// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strconv"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

func hp(t int64, v float64) zabbix.HistoryPoint {
	return zabbix.HistoryPoint{Clock: strconv.FormatInt(t, 10), Value: strconv.FormatFloat(v, 'f', -1, 64)}
}

// A restart is where the uptime counter starts over: the boot is the reading's time less its uptime,
// never before the reading before it; SNMP's counter wrapping after 497 days and a counter that read a
// few seconds back are not restarts.
func TestRestartsFromHistory(t *testing.T) {
	pts := []zabbix.HistoryPoint{hp(1180, 900000+180), hp(1000, 900000), hp(1360, 120), hp(1540, 300), hp(1720, 290)}
	got := restartsFromHistory(pts)
	if len(got) != 1 || got[0] != 1240 {
		t.Fatalf("one restart at 1240: %v", got)
	}
	// Booted, by its reading, before the last reading taken while it was up: clamped to that reading.
	if got := restartsFromHistory([]zabbix.HistoryPoint{hp(1000, 5000), hp(1060, 200)}); len(got) != 1 || got[0] != 1000 {
		t.Fatalf("clamped: %v", got)
	}
	if got := restartsFromHistory([]zabbix.HistoryPoint{hp(1000, snmpUptimeWrap-10), hp(1060, 50)}); len(got) != 0 {
		t.Fatalf("SNMP wrap read as a restart: %v", got)
	}
}

func tp(clock int64, mn, mx float64) zabbix.TrendPoint {
	return zabbix.TrendPoint{Clock: strconv.FormatInt(clock, 10), ValueMin: strconv.FormatFloat(mn, 'f', -1, 64), ValueMax: strconv.FormatFloat(mx, 'f', -1, 64)}
}

// From hourly trends: a restart inside an hour (its lowest far below its highest), one at the turn of
// the hour (below the hour before's highest), and hours without data in between that end without one.
func TestRestartsFromTrends(t *testing.T) {
	h := int64(3600)
	rows := []zabbix.TrendPoint{
		tp(0, 50000, 53540),
		tp(h, 53600, 57140),
		tp(2*h, 600, 60000), // restarted inside: up 600 s at its lowest
		tp(3*h, 4200, 7740),
		tp(4*h, 60, 3540), // restarted at the turn of the hour
		tp(9*h, 21660, 25200),
	}
	got := restartsFromTrends(rows)
	if len(got) != 2 || got[0] != 2*h+1200 || got[1] != 4*h+1740 {
		t.Fatalf("restarts: %v", got)
	}
}

// A chart shows its own sensors' entries and the host's, never another sensor's; an entry from before
// the log kept sensors is matched by the name it gave; a journal entry has its own marker.
func TestChangeMark(t *testing.T) {
	items := map[string]bool{"501": true}
	names := []string{"web1 · CPU"}
	cases := []struct {
		c    store.Change
		ok   bool
		kind string
	}{
		{store.Change{Action: "Paused a sensor", ItemsKnown: true, ItemIDs: []string{"501"}}, true, "change"},
		{store.Change{Action: "Paused a sensor", ItemsKnown: true, ItemIDs: []string{"502"}}, false, ""},
		{store.Change{Action: "Acknowledged", ItemsKnown: true, ItemIDs: []string{"502", "501"}}, true, "ack"},
		{store.Change{Action: "Changed host settings", ItemsKnown: true}, true, "change"},
		{store.Change{Action: "Left a note", Object: "web1 · CPU", Detail: "looking at it"}, true, "note"},
		{store.Change{Action: "Paused a sensor", Object: "web1 · Disk /"}, false, ""},
		{store.Change{Action: "Moved a host to another probe", Object: "web1"}, true, "change"},
		{store.Change{Action: "Added a journal entry", ItemsKnown: true}, false, ""},
	}
	for i, tc := range cases {
		m, ok := changeMark(tc.c, items, names)
		if ok != tc.ok || (ok && m.Kind != tc.kind) {
			t.Errorf("case %d (%s): got %v %q, want %v %q", i, tc.c.Action, ok, m.Kind, tc.ok, tc.kind)
		}
	}
	m, _ := changeMark(store.Change{Action: "Changed host settings", ItemsKnown: true, Actor: "Anna Example", Reason: "backup runs hot",
		Diff: []store.ChangeDiff{{Field: "CPU warning", Old: "80 %", New: "85 %"}, {Field: "Asset tag", New: "A-17"}}}, items, names)
	if m.What != "Changed host settings: CPU warning 80 % → 85 %; Asset tag A-17" || m.Who != "Anna Example" || m.Why != "backup runs hot" {
		t.Fatalf("text: %+v", m)
	}
}
