// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePast struct{}

func (fakePast) PastHistory(_ context.Context, itemID string, from, to int64) []PastPoint {
	if itemID != "new" {
		return nil
	}
	return []PastPoint{{Clock: 100, V: 1}, {Clock: 200, V: 0}}
}

func (fakePast) PastTrends(_ context.Context, itemID string, from, to int64) []PastTrend {
	if itemID != "new" {
		return nil
	}
	return []PastTrend{{Clock: 3600, Min: 0, Avg: 0.5, Max: 1, Num: 60}}
}

// A renamed sensor's reads carry its earlier name's kept readings, oldest first; text history and a
// read made WithoutPast (keeping them) don't.
func TestPastJoinsReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var res any
		switch req.Method {
		case "history.get":
			res = []map[string]string{{"itemid": "new", "clock": "300", "value": "1"}}
		case "trend.get":
			res = []map[string]string{{"itemid": "new", "clock": "7200", "num": "60", "value_min": "1", "value_avg": "1", "value_max": "1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": res, "id": req.ID})
	}))
	defer srv.Close()
	c := New(srv.URL, "token")
	c.SetPast(fakePast{})
	ctx := context.Background()

	h, err := c.History(ctx, "new", 3, 0, 1000)
	if err != nil || len(h) != 3 || h[0].Clock != "100" || h[1].Value != "0" || h[2].Clock != "300" {
		t.Fatalf("history: %v %v", h, err)
	}
	if h, _ := c.History(ctx, "new", 4, 0, 1000); len(h) != 1 {
		t.Fatalf("text history took kept readings: %v", h)
	}
	if h, _ := c.History(WithoutPast(ctx), "new", 3, 0, 1000); len(h) != 1 {
		t.Fatalf("WithoutPast took kept readings: %v", h)
	}
	tr, err := c.Trends(ctx, "new", 0, 10000)
	if err != nil || len(tr) != 2 {
		t.Fatalf("trends: %v %v", tr, err)
	}
	rows, err := c.TrendRows(ctx, []string{"new"}, 0, 10000)
	if err != nil || len(rows) != 2 || rows[1].ItemID != "new" || rows[1].Num != "60" || rows[1].ValueAvg != "0.5" {
		t.Fatalf("trend rows: %v %v", rows, err)
	}
	m, err := c.HistoryMulti(ctx, []string{"new"}, 0, 0)
	if err != nil || len(m) != 3 || m[0].ItemID != "new" || m[0].Clock != "100" {
		t.Fatalf("history multi: %v %v", m, err)
	}
}
