// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A problem on a hidden sensor is not news: the host's badge, count and colour leave it out, and the
// host's own problems list marks it hidden. A trigger over two sensors stays the host's while one of
// them is not hidden.
func TestHiddenSensorProblemsDontCount(t *testing.T) {
	hosts := []map[string]any{
		{"hostid": "101", "name": "nas1", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1"}}},
	}
	// Three active triggers on nas1: CPU overheating (sensor 11, error), Disk 1 running hot (sensor
	// 12, warning), and a pair trigger over sensors 12 and 13 (warning).
	triggers := []map[string]any{
		{"triggerid": "t1", "priority": "4", "value": "1", "hosts": []map[string]string{{"hostid": "101"}}, "items": []map[string]string{{"itemid": "11"}}},
		{"triggerid": "t2", "priority": "2", "value": "1", "hosts": []map[string]string{{"hostid": "101"}}, "items": []map[string]string{{"itemid": "12"}}},
		{"triggerid": "t3", "priority": "2", "value": "1", "hosts": []map[string]string{{"hostid": "101"}}, "items": []map[string]string{{"itemid": "12"}, {"itemid": "13"}}},
	}
	problems := []map[string]any{
		{"eventid": "e1", "name": "CPU overheating", "severity": "4", "clock": "1700000000", "acknowledged": "0", "objectid": "t1"},
		{"eventid": "e2", "name": "Disk 1 running hot", "severity": "2", "clock": "1700000000", "acknowledged": "0", "objectid": "t2"},
		{"eventid": "e3", "name": "Disks running hot", "severity": "2", "clock": "1700000000", "acknowledged": "0", "objectid": "t3"},
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		switch req.Method {
		case "host.get":
			result = hosts
		case "trigger.get":
			result = triggers
		case "problem.get":
			result = problems
		case "hostgroup.get":
			result = []map[string]any{{"groupid": "1", "name": "site1", "hosts": "1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer mock.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	admin := &store.User{ID: 1, Role: "admin"}
	call := func(h http.HandlerFunc, path, id string) []byte {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if id != "" {
			req.SetPathValue("id", id)
		}
		req = req.WithContext(auth.WithUser(req.Context(), admin))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Body.Bytes()
	}
	badge := func() hostView {
		var got []hostView
		_ = json.Unmarshal(call(s.handleHosts, "/api/hosts", ""), &got)
		if len(got) != 1 {
			t.Fatalf("expected one host, got %+v", got)
		}
		return got[0]
	}
	list := func() map[string]problemView {
		var got []problemView
		_ = json.Unmarshal(call(s.handleHostProblems, "/api/hosts/101/problems", "101"), &got)
		out := map[string]problemView{}
		for _, p := range got {
			out[p.EventID] = p
		}
		return out
	}

	if hv := badge(); hv.Problems != 3 || hv.State != "error" {
		t.Fatalf("nothing hidden: want 3 problems, error; got %d, %s", hv.Problems, hv.State)
	}

	// Hide the CPU temperature sensor: its error is no longer the host's.
	if err := st.SetSuppression(context.Background(), "hide", "item", "11", 1, "", nil); err != nil {
		t.Fatal(err)
	}
	if hv := badge(); hv.Problems != 2 || hv.State != "warning" {
		t.Errorf("CPU sensor hidden: want 2 problems, warning; got %d, %s", hv.Problems, hv.State)
	}
	got := list()
	if !got["e1"].Hidden || got["e2"].Hidden || got["e3"].Hidden {
		t.Errorf("host list: want only CPU overheating hidden, got e1=%v e2=%v e3=%v", got["e1"].Hidden, got["e2"].Hidden, got["e3"].Hidden)
	}

	// Hide sensor 12 too: Disk 1 goes, but the pair trigger still reads sensor 13.
	if err := st.SetSuppression(context.Background(), "hide", "item", "12", 1, "", nil); err != nil {
		t.Fatal(err)
	}
	if hv := badge(); hv.Problems != 1 || hv.State != "warning" {
		t.Errorf("two sensors hidden: want 1 problem, warning; got %d, %s", hv.Problems, hv.State)
	}
	got = list()
	if !got["e2"].Hidden || got["e3"].Hidden {
		t.Errorf("host list: want Disk 1 hidden and the pair not, got e2=%v e3=%v", got["e2"].Hidden, got["e3"].Hidden)
	}

	// A hidden host: everything on its page reads hidden; the badge still carries its state (the
	// tree dims the host as a whole).
	if err := st.SetSuppression(context.Background(), "hide", "host", "101", 1, "", nil); err != nil {
		t.Fatal(err)
	}
	for id, p := range list() {
		if !p.Hidden {
			t.Errorf("hidden host: problem %s not marked hidden", id)
		}
	}
	if hv := badge(); !hv.Hidden {
		t.Errorf("hidden host: badge not flagged hidden")
	}
}
