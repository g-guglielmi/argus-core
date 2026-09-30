// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Zabbix's problem events and Argus's own incidents come back as one list, newest first: closed ones
// with their end, open ones without, info-level events left out, the sensor each was on, the site,
// who acknowledged, and for a collector flag the reason it gave when it started.
func TestCollectIncidents(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		host := []map[string]string{{"hostid": "10", "name": "nas1", "status": "0"}}
		switch req.Method {
		case "event.get":
			if _, recov := req.Params["eventids"]; recov {
				result = []map[string]string{{"eventid": "501", "clock": "2500"}}
				break
			}
			if obj, ok := req.Params["objectids"].([]any); ok { // a sensor's own triggers only
				if len(obj) == 1 && obj[0] == "902" {
					result = []map[string]any{{"eventid": "1", "clock": "2000", "name": "High CPU utilization", "severity": "2", "r_eventid": "501", "objectid": "902", "hosts": host}}
				}
				break
			}
			result = []map[string]any{
				{"eventid": "3", "clock": "3000", "name": "SSH monitoring is unreachable", "severity": "4", "r_eventid": "0", "objectid": "900", "hosts": host},
				{"eventid": "2", "clock": "2200", "name": "Info only", "severity": "1", "r_eventid": "0", "objectid": "901", "hosts": host},
				{"eventid": "1", "clock": "2000", "name": "High CPU utilization", "severity": "2", "r_eventid": "501", "objectid": "902", "hosts": host},
			}
		case "trigger.get":
			if ids, byItem := req.Params["itemids"].([]any); byItem {
				result = []map[string]any{}
				if len(ids) == 1 && ids[0] == "71" {
					result = []map[string]any{{"triggerid": "902", "status": "0", "priority": "2", "expression": "x", "items": []map[string]string{{"itemid": "71"}}}}
				}
				break
			}
			result = []map[string]any{
				{"triggerid": "900", "items": []map[string]string{{"itemid": "70"}}},
				{"triggerid": "902", "items": []map[string]string{{"itemid": "71"}}},
			}
		case "item.get":
			if f, ok := req.Params["filter"].(map[string]any); ok && f["key_"] == "linux.ssh.error" {
				result = []map[string]string{{"itemid": "72"}}
				break
			}
			result = []map[string]string{
				{"itemid": "70", "hostid": "10", "name": "Monitoring reachable", "key_": "linux.ssh.reachable"},
				{"itemid": "71", "hostid": "10", "name": "CPU utilization", "key_": "system.cpu.util[ssh]"},
				{"itemid": "80", "hostid": "10", "name": "Uptime", "key_": "system.uptime[ssh]"},
			}
		case "history.get":
			result = []map[string]string{
				{"clock": "2000", "value": ""},
				{"clock": "2990", "value": "root@nas1: Permission denied (publickey)."},
			}
		case "host.get":
			result = []map[string]any{{"hostid": "10", "name": "nas1", "status": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1"}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer mock.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, store.User{Email: "ops@example.com", Name: "Ops", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSuppression(ctx, "ack", "event", "1", uid, "looking", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArgusIncidents(ctx, []store.OpenArgusIncident{{EventID: "argus-unsupported-80", HostID: "10", HostName: "nas1",
		ItemID: "80", Name: "Uptime stopped collecting", Severity: 4, Reason: "Timeout", StartedAt: 2600}}, 2700); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token")}

	got, err := s.collectIncidents(ctx, []string{"10"}, nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 incidents (the info event left out), got %+v", got)
	}
	ssh, argus, cpu := got[0], got[1], got[2]
	if ssh.EventID != "3" || ssh.End != 0 || ssh.Sensor != "Monitoring reachable" || ssh.Reason != "root@nas1: Permission denied (publickey)." || ssh.Site != "site1" {
		t.Fatalf("open flag incident wrong: %+v", ssh)
	}
	if !argus.Argus || argus.EventID != "argus-unsupported-80" || argus.Reason != "Timeout" || argus.End != 0 {
		t.Fatalf("Argus incident wrong: %+v", argus)
	}
	if cpu.EventID != "1" || cpu.End != 2500 || cpu.AckBy != "Ops" || cpu.AckNote != "looking" || cpu.Reason != "" {
		t.Fatalf("closed, acknowledged incident wrong: %+v", cpu)
	}

	// A drilled-down sensor: only its own triggers' events, and only its own Argus incidents.
	only, err := s.collectIncidents(ctx, []string{"10"}, []string{"71"}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].EventID != "1" {
		t.Fatalf("sensor 71: want just its CPU incident, got %+v", only)
	}
	argusOnly, err := s.collectIncidents(ctx, []string{"10"}, []string{"80"}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(argusOnly) != 1 || argusOnly[0].EventID != "argus-unsupported-80" {
		t.Fatalf("sensor 80 (no trigger): want just its Argus incident, got %+v", argusOnly)
	}
}
