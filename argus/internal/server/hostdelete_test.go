// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Deleting a host deletes it in Zabbix, then forgets what Argus keeps on it and its sensors: an
// action aimed at its sensor (with its Zabbix script), its sensor's priority, its place in a saved
// report. Another host's records stay. A probe's own host is refused.
func TestDeleteHost(t *testing.T) {
	var deleted, scripts []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params any    `json:"params"`
			ID     any    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		switch req.Method {
		case "item.get":
			result = []map[string]string{{"itemid": "501", "hostid": "50", "name": "CPU", "key_": "system.cpu.util"}}
		case "host.delete":
			for _, id := range req.Params.([]any) {
				deleted = append(deleted, id.(string))
			}
			result = map[string]any{"hostids": req.Params}
		case "script.delete":
			for _, id := range req.Params.([]any) {
				scripts = append(scripts, id.(string))
			}
			result = map[string]any{"scriptids": req.Params}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	t.Cleanup(mock.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	aid, err := st.SaveAction(ctx, store.Action{Name: "Restart", Type: "service", Scope: "item", ItemID: "501", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetActionScript(ctx, aid, "77")
	keep, _ := st.SaveAction(ctx, store.Action{Name: "Other", Type: "service", Scope: "item", ItemID: "999", Config: "{}"})
	_ = st.SetItemPriority(ctx, "501", 5, 0)
	_ = st.SetItemPriority(ctx, "999", 5, 0)
	if _, err := st.CreateReport(ctx, store.Report{Name: "Monthly", Spec: store.ReportSpec{Hosts: []string{"50", "60"},
		Sensors: []store.ReportSensor{{HostID: "50", Items: []string{"501"}}, {HostID: "60", Items: []string{"601"}}}}}); err != nil {
		t.Fatal(err)
	}

	_ = st.UpsertNotifyState(ctx, store.NotifyState{EventID: "e50", HostID: "50", Name: "Down", Severity: 4, State: "firing", FirstSeen: 1})
	_ = st.UpsertNotifyState(ctx, store.NotifyState{EventID: "e60", HostID: "60", Name: "Down", Severity: 4, State: "firing", FirstSeen: 1})
	_ = st.UpsertNotifyDelivery(ctx, store.NotifyDelivery{EventID: "e50", Kind: store.DeliveryGlobal, ChannelID: 1})
	_ = st.QueueOutbox(ctx, store.Outbox{EventID: "e50", DestKey: "g:1", Kind: "problem", Event: []byte("{}")})
	t.Cleanup(func() { deletedHosts.Delete("50") })

	if err := s.deleteHost(ctx, "50"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "50" {
		t.Fatalf("Zabbix deleted %v", deleted)
	}
	if len(scripts) != 1 || scripts[0] != "77" {
		t.Fatalf("the action's script: deleted %v", scripts)
	}
	if _, err := st.ActionByID(ctx, aid); err == nil {
		t.Fatal("the action aimed at its sensor stayed")
	}
	if _, err := st.ActionByID(ctx, keep); err != nil {
		t.Fatal("another host's action went")
	}
	prio, _ := st.ItemPriorities(ctx)
	if _, ok := prio["501"]; ok || prio["999"] != 5 {
		t.Fatalf("priorities: %v", prio)
	}
	if states, _ := st.NotifyStates(ctx); len(states) != 1 || states["e60"].HostID != "60" {
		t.Fatalf("alert states after the delete: %v", states)
	}
	if dl, _ := st.NotifyDeliveries(ctx); len(dl["e50"]) != 0 {
		t.Fatal("the deleted host's deliveries stayed")
	}
	if ob, _ := st.OutboxAll(ctx); len(ob) != 0 {
		t.Fatalf("the deleted host's outbox stayed: %v", ob)
	}
	if !hostDeleted("50") {
		t.Fatal("the deleted host is not marked for a notifier pass already running")
	}
	reps, _ := st.Reports(ctx)
	if sp := reps[0].Spec; len(sp.Hosts) != 1 || sp.Hosts[0] != "60" || len(sp.Sensors) != 1 || sp.Sensors[0].HostID != "60" {
		t.Fatalf("report spec: %+v", sp)
	}

	_ = st.SetDeviceClass(ctx, "70", "probe", "manual")
	if err := s.deleteHost(ctx, "70"); !errors.Is(err, errProbeHost) || len(deleted) != 1 {
		t.Fatalf("a probe's own host: err %v, deleted %v", err, deleted)
	}
}
