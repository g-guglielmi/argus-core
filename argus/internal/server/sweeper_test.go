// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// A timed pause ends with the sweeper re-enabling the host or sensor in Zabbix. One that was deleted
// while paused can never be re-enabled: its pause is dropped, said once, where the sweeper used to
// warn every minute for good. A Zabbix that fails for another reason keeps the pause for the next try.
func TestSweeperDropsAPauseWhoseObjectIsGone(t *testing.T) {
	down := false
	var updates []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id, _ := req.Params["hostid"].(string)
		if id == "" {
			id, _ = req.Params["itemid"].(string)
		}
		updates = append(updates, req.Method+" "+id)
		switch {
		case down:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32500, "message": "Application error.", "data": "Internal error."}})
		case id == "501" || id == "901": // deleted in Zabbix while they were paused
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32500, "message": "Application error.", "data": "No permissions to referred object or it does not exist!"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		}
	}))
	t.Cleanup(mock.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	past := time.Now().Add(-time.Minute).Unix()
	pause := func(scope, id string) {
		t.Helper()
		if err := st.SetSuppression(ctx, "pause", scope, id, 0, "", &past); err != nil {
			t.Fatal(err)
		}
	}
	left := func() string {
		t.Helper()
		rows, err := st.ExpiredPauses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Scope+" "+r.TargetID)
		}
		return strings.Join(out, ", ")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	zbx := zabbix.New(mock.URL, "test-token")

	// Zabbix fails for a reason of its own: every pause stays for the next tick.
	pause("host", "500")
	pause("host", "501")
	pause("item", "901")
	down = true
	sweepOnce(ctx, st, zbx, logger)
	if got := left(); strings.Count(got, ",") != 2 {
		t.Fatalf("a failing Zabbix must keep every pause, left: %q", got)
	}
	if !strings.Contains(logs.String(), "will retry next tick") {
		t.Fatalf("a failure to retry is still a warning: %s", logs.String())
	}

	// Zabbix answers: the live host is re-enabled, the two deleted ones lose their pause.
	down = false
	logs.Reset()
	updates = nil
	sweepOnce(ctx, st, zbx, logger)
	if got := left(); got != "" {
		t.Fatalf("no pause is left to retry, got %q", got)
	}
	if n := strings.Count(logs.String(), "its pause is dropped"); n != 2 {
		t.Fatalf("each dropped pause is said once, got %d: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), "will retry next tick") {
		t.Fatalf("a deleted object is not a failure to retry: %s", logs.String())
	}
	if len(updates) != 3 {
		t.Fatalf("each pause was tried once, got %v", updates)
	}

	// And the next tick has nothing to ask Zabbix.
	updates = nil
	sweepOnce(ctx, st, zbx, logger)
	if len(updates) != 0 {
		t.Fatalf("a dropped pause is not tried again, got %v", updates)
	}
}
