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

// A dependent sensor runs the item that does its reading: its master, up the chain; an item that reads
// on its own runs itself.
func TestReadingItems(t *testing.T) {
	type item struct{ ID, Type, Master string }
	db := map[string]item{
		"1": {"1", "10", "0"}, // the speed test (external check)
		"2": {"2", "18", "1"}, // its download speed
		"3": {"3", "18", "2"}, // a reading of a reading
		"4": {"4", "3", "0"},  // a simple check
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int `json:"id"`
			Params struct {
				ItemIDs []string `json:"itemids"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var res []map[string]string
		for _, id := range req.Params.ItemIDs {
			if it, ok := db[id]; ok {
				res = append(res, map[string]string{"itemid": it.ID, "type": it.Type, "master_itemid": it.Master})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": res, "id": req.ID})
	}))
	defer srv.Close()
	c := New(srv.URL, "token")
	got, err := c.ReadingItems(context.Background(), []string{"2", "3", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if got["2"] != "1" || got["3"] != "1" || got["4"] != "4" {
		t.Fatalf("got %v", got)
	}
}
