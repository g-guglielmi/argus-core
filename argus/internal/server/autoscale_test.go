// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestDecideProcs(t *testing.T) {
	pingers, _ := procSpecByName("StartPingers")
	syncers, _ := procSpecByName("StartDBSyncers")
	cases := []struct {
		sp   procSpec
		cur  int
		peak float64
		want int
	}{
		{pingers, 5, 72, 8},   // raise: 5 * 72 / 50 = 7.2 -> 8
		{pingers, 5, 100, 10}, // saturated: double
		{pingers, 5, 61, 7},   // just over the raise line: 6.1 -> 7
		{pingers, 5, 59, 5},   // inside the band: keep
		{pingers, 20, 10, 5},  // lower: 20 * 10 / 50 = 4, but never under the default 5
		{pingers, 12, 18, 5},  // 12 * 18 / 50 = 4.3 -> 5
		{pingers, 5, 5, 5},    // quiet but already at the default
		{pingers, 48, 90, 50}, // capped at the ceiling
		{pingers, 60, 90, 60}, // above the ceiling already (set before): left alone
		{syncers, 4, 95, 8},   // 4 * 95 / 50 = 7.6 -> 8 = the ceiling
		{syncers, 8, 99, 8},   // at the ceiling
		{pingers, 0, 90, 0},   // not running: nothing to scale
	}
	for _, c := range cases {
		if got := decideProcs(c.sp, c.cur, c.peak); got != c.want {
			t.Errorf("%s cur=%d peak=%.0f: got %d, want %d", c.sp.Name, c.cur, c.peak, got, c.want)
		}
	}
}

// Every kind Argus sizes must match a busy item of the Probe health template key for key, or its
// load would never be found.
func TestProcSpecsMatchTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "provision", "templates", "probe-health.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range procSpecs {
		if !strings.Contains(string(raw), "key: '"+sp.busyKey()+"'") {
			t.Errorf("%s: no item %s in the Probe health template", sp.Name, sp.busyKey())
		}
	}
}

// The proxy reports its counts; the next check-in hands the target back; a change Argus made is
// handed out, and the restart goes only to a sidecar that can restart, once.
func TestProbeCheckinProcs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const raw = "probe-token"
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	checkin := func(body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleProbeCheckin(w, r)
		if w.Code != 200 {
			t.Fatalf("check-in: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	// Unknown kinds and pinned counts are dropped from the target.
	out := checkin(`{"version":"7.0.31-r8","scans":true,"procs":{"StartPingers":5,"StartPollers":5,"StartBogus":3},"procs_pinned":["StartPollers"]}`)
	procs, _ := out["procs"].(map[string]any)
	if len(procs) != 1 || procs["StartPingers"] != float64(5) {
		t.Fatalf("seeded target handed out: %v", out["procs"])
	}

	// Argus raises the pingers; the sidecar that can restart gets a one-shot, once.
	if err := st.SetProbeProcsEvaluation(ctx, "proxy-site1", map[string]float64{"StartPingers": 72}, map[string]int{"StartPingers": 8}, "ICMP pingers 5 -> 8", true); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	var restart bool
	yes, no := true, false
	s.procsHandout(ctx, "proxy-site1", &yes, &no, &got, &restart)
	if got["StartPingers"] != 8 || restart {
		t.Fatalf("a sidecar that can't restart: procs=%v restart=%v", got, restart)
	}
	s.procsHandout(ctx, "proxy-site1", &yes, &yes, &got, &restart)
	if !restart {
		t.Fatal("a sidecar that can restart should get the restart")
	}
	restart = false
	s.procsHandout(ctx, "proxy-site1", &yes, &yes, &got, &restart)
	if restart {
		t.Fatal("the restart must be handed out once per gap")
	}

	// Off: nothing is handed out.
	if err := mgr.Set(ctx, map[string]string{settings.KeyAutoscale: settings.AutoscaleOff}); err != nil {
		t.Fatal(err)
	}
	if out := checkin(`{"version":"7.0.31-r8","scans":true,"procs":{"StartPingers":5}}`); out["procs"] != nil {
		t.Fatalf("off still hands out counts: %v", out["procs"])
	}
}
