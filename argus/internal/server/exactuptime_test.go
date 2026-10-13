// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"argus/internal/zabbix"
)

// Zabbix keeps the hourly average of a whole-number sensor (the ping) rounded down, so an hour with
// three missed checks in sixty reads 0, as if down all of it. exactUptime reads such hours again from
// the raw checks: 57 of 60 up. An hour the raw history no longer has stays as read and is counted; an
// hour that read 1, and a sensor kept with decimals, aren't touched.
func TestExactUptime(t *testing.T) {
	const h1, h2, h3 = 7200, 10800, 3600 * 30 // hours: one with raw checks, one too old, one up
	asked := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		if req.Method == "history.get" {
			asked++
			from, _ := req.Params["time_from"].(float64)
			to, _ := req.Params["time_till"].(float64)
			var out []map[string]string
			for c := int64(h1); c < h1+3600; c += 60 { // the raw checks of h1 only: 3 down
				if c < int64(from) || c > int64(to) {
					continue
				}
				v := "1"
				if c >= h1+600 && c < h1+780 {
					v = "0"
				}
				out = append(out, map[string]string{"clock": strconv.FormatInt(c, 10), "value": v})
			}
			result = out
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer mock.Close()
	s := &Server{zbx: zabbix.New(mock.URL, "test-token")}
	rows := []zabbix.TrendRow{
		{ItemID: "1", Clock: strconv.Itoa(h1), Num: "60", ValueAvg: "0"},
		{ItemID: "1", Clock: strconv.Itoa(h2), Num: "60", ValueAvg: "0"},
		{ItemID: "1", Clock: strconv.Itoa(h3), Num: "60", ValueAvg: "1"},
		{ItemID: "2", Clock: strconv.Itoa(h1), Num: "60", ValueAvg: "0"},
	}
	got, stale := s.exactUptime(t.Context(), rows, map[string]string{"1": "3", "2": "0"})
	if got[0].ValueAvg != "0.95" || got[0].Num != "60" {
		t.Fatalf("an hour with 3 of 60 checks down: %+v", got[0])
	}
	if got[1].ValueAvg != "0" || stale != 1 {
		t.Fatalf("an hour older than the raw checks stays as read and is counted: %+v, stale %d", got[1], stale)
	}
	if got[2].ValueAvg != "1" || got[3].ValueAvg != "0" {
		t.Fatalf("an hour up, and a sensor kept with decimals, stay as they were: %+v %+v", got[2], got[3])
	}
	if rows[0].ValueAvg != "0" {
		t.Fatal("the rows passed in were changed")
	}
	if asked != 1 {
		t.Fatalf("hours close together are read in one go: %d history reads", asked)
	}
}
