// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Trends fill whole hours, raw history the hour they don't cover yet; 24 h comes from history
// alone; days are local calendar days; the strip keeps the last 60 checks.
func TestBuildAvailability(t *testing.T) {
	loc := time.FixedZone("test", 2*3600)
	now := time.Date(2026, 9, 30, 15, 20, 0, 0, loc)
	today := startOfDay(now, loc)
	var trends []zabbix.TrendRow
	// Yesterday: all up. Today 00:00-15:00: down for the 10:00 hour, up otherwise.
	for h := today.AddDate(0, 0, -1); h.Before(today.Add(15 * time.Hour)); h = h.Add(time.Hour) {
		avg := "1"
		if h.Equal(today.Add(10 * time.Hour)) {
			avg = "0"
		}
		trends = append(trends, zabbix.TrendRow{ItemID: "1", Clock: fmt.Sprint(h.Unix()), Num: "60", ValueAvg: avg})
	}
	// History: one check a minute over the last 24 h, down 10:00-10:59 and 15:10-15:19.
	var hist []zabbix.HistoryPoint
	for m := now.Add(-24 * time.Hour); m.Before(now); m = m.Add(time.Minute) {
		v := "1"
		if (m.After(today.Add(10*time.Hour)) || m.Equal(today.Add(10*time.Hour))) && m.Before(today.Add(11*time.Hour)) {
			v = "0"
		}
		if !m.Before(today.Add(15*time.Hour + 10*time.Minute)) {
			v = "0"
		}
		hist = append(hist, zabbix.HistoryPoint{Clock: fmt.Sprint(m.Unix()), Value: v})
	}
	v := buildAvailability("1", trends, hist, now, loc)

	if v.Uptime24 == nil || *v.Uptime24 != 95.139 { // 1440 checks, 70 down
		t.Fatalf("24h = %v, want 95.139", deref(v.Uptime24))
	}
	// Today: 15 trend hours (1 down) = 900 checks, 60 down; plus 15:00-15:19 from history, 10 down.
	wantToday := 100 * float64(900-60+20-10) / float64(900+20)
	last := v.Days[len(v.Days)-1]
	if last.Day != "2026-09-30" || last.Pct == nil || abs(*last.Pct-wantToday) > 0.001 {
		t.Fatalf("today = %v %v, want %.3f", last.Day, deref(last.Pct), wantToday)
	}
	if y := v.Days[len(v.Days)-2]; y.Pct == nil || *y.Pct != 100 {
		t.Fatalf("yesterday = %v, want 100", deref(y.Pct))
	}
	if len(v.Days) != uptimeWindowDays || v.Days[0].Pct != nil {
		t.Fatalf("want %d days, the oldest without data: %+v", uptimeWindowDays, v.Days[0])
	}
	want7 := 100 * float64(1440+900-60+20-10) / float64(1440+900+20)
	if v.Uptime7 == nil || abs(*v.Uptime7-want7) > 0.001 || v.Uptime30 == nil || *v.Uptime30 != *v.Uptime7 {
		t.Fatalf("7d = %v 30d = %v, want %.3f both", deref(v.Uptime7), deref(v.Uptime30), want7)
	}
	if len(v.Checks) != uptimeChecks || v.Checks[len(v.Checks)-1][1] != 0 || v.Checks[0][1] != 1 {
		t.Fatalf("checks strip wrong: %d, first %v last %v", len(v.Checks), v.Checks[0], v.Checks[len(v.Checks)-1])
	}
}

func TestHostUptimeItem(t *testing.T) {
	ping := zabbix.Item{ItemID: "1", Key: "icmpping", Status: "0"}
	cpu := zabbix.Item{ItemID: "2", Key: "system.cpu.util", Status: "0"}
	ssh := zabbix.Item{ItemID: "3", Key: "linux.ssh.reachable", Status: "0"}
	cases := []struct {
		items    []zabbix.Item
		override string
		want     string
	}{
		{[]zabbix.Item{ping, cpu, ssh}, "", "1"},
		{[]zabbix.Item{ping, cpu, ssh}, "3", "3"}, // an up/down master wins
		{[]zabbix.Item{ping, cpu, ssh}, "2", "1"}, // a graded master can't give an uptime: ping
		{[]zabbix.Item{{ItemID: "1", Key: "icmpping", Status: "1"}, ssh}, "", "3"},
		{[]zabbix.Item{cpu}, "", ""},
	}
	for i, c := range cases {
		got, ok := hostUptimeItem(c.items, c.override)
		if (c.want == "") == ok || (ok && got.ItemID != c.want) {
			t.Errorf("case %d: got %q ok=%v, want %q", i, got.ItemID, ok, c.want)
		}
	}
}

func TestStatusUptimeFor(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	ups := map[string]hostUptime{
		"a": {D7: p(100), D30: p(100)},
		"b": {D7: p(99), D30: p(98.5)},
		"c": {D7: p(100), D30: p(99.9)},
		"d": {}, // no data
	}
	v := statusUptimeFor(ups, map[string]string{"a": "site1", "b": "site1", "c": "site2"}, map[string]string{"a": "gw", "b": "nas", "c": "ap"})
	if v.Hosts != 3 || v.Overall30 == nil || *v.Overall30 != 99.467 {
		t.Fatalf("overall = %v over %d hosts, want 99.467 over 3", deref(v.Overall30), v.Hosts)
	}
	if len(v.Below) != 2 || v.Below[0].Host != "nas" || v.Below[1].Host != "ap" || v.Below[0].Site != "site1" {
		t.Fatalf("below wrong: %+v", v.Below)
	}
}

// Complete days are read from Zabbix once and stored; afterwards only the days still open are read,
// and a new sensor reads its window without making the others read theirs again.
func TestUptimeDaysFor(t *testing.T) {
	loc := time.FixedZone("test", 2*3600)
	now := time.Date(2026, 9, 30, 15, 0, 0, 0, loc)
	today := startOfDay(now, loc)
	downDay := today.AddDate(0, 0, -3)

	var mu sync.Mutex
	type call struct {
		items []any
		from  int64
	}
	var calls []call
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := []map[string]string{}
		if req.Method == "trend.get" {
			from := int64(asF(req.Params["time_from"]))
			till := int64(asF(req.Params["time_till"]))
			items, _ := req.Params["itemids"].([]any)
			mu.Lock()
			calls = append(calls, call{items, from})
			mu.Unlock()
			for _, it := range items {
				id := fmt.Sprint(it)
				for h := time.Unix(from, 0); h.Unix() <= till && h.Before(now.Truncate(time.Hour)); h = h.Add(time.Hour) {
					avg := "1"
					if id == "2" && !h.Before(downDay) && h.Before(downDay.AddDate(0, 0, 1)) {
						avg = "0.5"
					}
					result = append(result, map[string]string{"itemid": id, "clock": fmt.Sprint(h.Unix()), "num": "60", "value_avg": avg})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer mock.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token")}
	reset := func() {
		uptimeDaysCache.mu.Lock()
		uptimeDaysCache.m = map[string]uptimeDaysEntry{}
		uptimeDaysCache.mu.Unlock()
		mu.Lock()
		calls = nil
		mu.Unlock()
	}
	window := windowDays(now, loc, uptimeWindowDays)
	reset()

	got := s.uptimeDaysFor(t.Context(), []string{"1", "2"}, now, loc)
	if p := sumDays(got["1"], window, 30).pct(); p == nil || *p != 100 {
		t.Fatalf("item 1 = %v, want 100", deref(p))
	}
	// Item 2: 29 full days + 15 h today, one day at half.
	want := 100 * (float64(29*24*60+15*60) - 12*60) / float64(29*24*60+15*60)
	if p := sumDays(got["2"], window, 30).pct(); p == nil || abs(*p-want) > 0.001 {
		t.Fatalf("item 2 = %v, want %.3f", deref(p), want)
	}
	rows, _ := st.UptimeDays(t.Context(), []string{"1", "2"}, window[0])
	if len(rows) != 2*29 { // today is not complete, so not stored
		t.Fatalf("stored %d days, want %d", len(rows), 2*29)
	}

	reset()
	again := s.uptimeDaysFor(t.Context(), []string{"1", "2"}, now, loc)
	if p := sumDays(again["2"], window, 30).pct(); p == nil || abs(*p-want) > 0.001 {
		t.Fatalf("item 2 from the store = %v, want %.3f", deref(p), want)
	}
	for _, c := range calls {
		if c.from != today.Unix() {
			t.Fatalf("second pass read Zabbix from %v, want only today (%v)", time.Unix(c.from, 0).In(loc), today)
		}
	}

	reset()
	_ = s.uptimeDaysFor(t.Context(), []string{"1", "2", "3"}, now, loc)
	for _, c := range calls {
		if c.from != today.Unix() && (len(c.items) != 1 || fmt.Sprint(c.items[0]) != "3") {
			t.Fatalf("a new sensor made others read their window again: %+v", c)
		}
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
