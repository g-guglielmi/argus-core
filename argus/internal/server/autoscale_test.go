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
	"time"

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

	// The CPU report is stored; one out of shape is ignored.
	checkin(`{"version":"7.0.31-r9","scans":true,"cpu":{"count":2,"quota":1.5,"load":[0.8,0.6,0.5]}}`)
	checkin(`{"version":"7.0.31-r9","scans":true,"cpu":{"count":2,"quota":0,"load":[-1,0.6]}}`)
	if ag, err := st.ProbeAgentByName(ctx, "proxy-site1"); err != nil || ag.Procs.CPU.Count != 2 || ag.Procs.CPU.Effective() != 1.5 || ag.Procs.CPU.Load1 != 0.8 {
		t.Fatalf("cpu report: %+v %v", ag.Procs.CPU, err)
	}

	// Argus raises the pingers; the sidecar that can restart gets a one-shot, once.
	if err := st.SetProbeProcsEvaluation(ctx, "proxy-site1", store.ProcsEvaluation{Peaks: map[string]float64{"StartPingers": 72}, Target: map[string]int{"StartPingers": 8}, CPUPeak: -1, Note: "ICMP pingers 5 -> 8"}); err != nil {
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

// planProcs: every raise is remembered and judged at the next evaluation; one that didn't bring the
// load down is put back and held; a new CPU count releases the hold; short on CPU, nothing is raised.
func TestPlanProcs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base := store.ProbeProcs{
		Running: map[string]int{"StartPingers": 5, "StartPollers": 5},
		Target:  map[string]int{"StartPingers": 5, "StartPollers": 5},
		CPU:     store.ProbeCPU{Count: 2},
	}

	// 1. A busy kind is raised, and the raise is remembered.
	ev := planProcs(base, map[string]float64{"StartPingers": 72, "StartPollers": 30}, 0.4, 6, now)
	if ev.Target["StartPingers"] != 8 || ev.Target["StartPollers"] != 5 {
		t.Fatalf("raise: %v", ev.Target)
	}
	ch, ok := ev.Changes["StartPingers"]
	if !ok || ch.From != 5 || ch.To != 8 || ch.Peak != 72 || ev.Note == "" {
		t.Fatalf("raise remembered: %+v note=%q", ev.Changes, ev.Note)
	}

	// 2. Applied and the load came down as predicted: no hold, the judged raise is dropped.
	applied := base
	applied.Running = map[string]int{"StartPingers": 8, "StartPollers": 5}
	applied.Target = ev.Target
	applied.Changes = ev.Changes
	ok2 := planProcs(applied, map[string]float64{"StartPingers": 46, "StartPollers": 30}, 0.4, 6, now.Add(7*time.Hour))
	if len(ok2.Held) != 0 || len(ok2.Changes) != 0 || ok2.Target["StartPingers"] != 8 || ok2.Note != "" {
		t.Fatalf("helped: %+v", ok2)
	}

	// 3. Applied but the load barely moved: put back to 5 and held, with the CPUs it had.
	bad := planProcs(applied, map[string]float64{"StartPingers": 70, "StartPollers": 30}, 0.4, 6, now.Add(7*time.Hour))
	h, held := bad.Held["StartPingers"]
	if bad.Target["StartPingers"] != 5 || !held || h.CPUs != 2 || h.From != 5 || h.To != 8 || h.Before != 72 || h.After != 70 {
		t.Fatalf("rollback: target=%v held=%+v", bad.Target, bad.Held)
	}
	if !strings.Contains(bad.Note, "back to 5") {
		t.Fatalf("rollback note: %q", bad.Note)
	}

	// 4. Held: not raised again, however busy.
	heldP := base
	heldP.Held = bad.Held
	again := planProcs(heldP, map[string]float64{"StartPingers": 95}, 0.4, 6, now.Add(14*time.Hour))
	if again.Target["StartPingers"] != 5 || len(again.Held) != 1 {
		t.Fatalf("held kind raised: %v", again.Target)
	}

	// 5. The probe got more CPUs: the hold is released and the kind judged (and raised) again.
	moreCPU := heldP
	moreCPU.CPU = store.ProbeCPU{Count: 4}
	lifted := planProcs(moreCPU, map[string]float64{"StartPingers": 95}, 0.4, 6, now.Add(14*time.Hour))
	if len(lifted.Held) != 0 || lifted.Target["StartPingers"] != 10 || !strings.Contains(lifted.Note, "released") {
		t.Fatalf("release on a new CPU count: %+v", lifted)
	}

	// 6. Short on CPU: nothing is raised, but a quiet kind may still come down.
	quiet := base
	quiet.Running = map[string]int{"StartPingers": 5, "StartPollers": 20}
	quiet.Target = map[string]int{"StartPingers": 5, "StartPollers": 20}
	starved := planProcs(quiet, map[string]float64{"StartPingers": 90, "StartPollers": 10}, 1.4, 6, now)
	if !starved.Starved || starved.Target["StartPingers"] != 5 || starved.Target["StartPollers"] != 5 {
		t.Fatalf("starved: %+v", starved)
	}
	// Too few hours of load reports: not judged as starved.
	if planProcs(base, map[string]float64{"StartPingers": 90}, 1.4, 2, now).Starved {
		t.Fatal("two hours of load reports must not count as short on CPU")
	}
}

func TestBusiestLoadPerCPU(t *testing.T) {
	var samples []store.LoadSample
	for m := 0; m < 60; m++ { // hour 0: load 1.0 on 2 CPUs
		samples = append(samples, store.LoadSample{At: int64(m * 60), Load1: 1.0, CPUs: 2})
	}
	for m := 0; m < 60; m++ { // hour 1: load 3.0 on 2 CPUs
		samples = append(samples, store.LoadSample{At: 3600 + int64(m*60), Load1: 3.0, CPUs: 2})
	}
	for m := 0; m < 10; m++ { // hour 2: too few samples to count
		samples = append(samples, store.LoadSample{At: 7200 + int64(m*60), Load1: 9.0, CPUs: 2})
	}
	peak, hours := busiestLoadPerCPU(samples)
	if peak != 1.5 || hours != 2 {
		t.Fatalf("peak=%v hours=%d, want 1.5 over 2 hours", peak, hours)
	}
	if p, h := busiestLoadPerCPU(nil); p != -1 || h != 0 {
		t.Fatalf("no samples: %v %d", p, h)
	}
}

func TestProbeCPUNotice(t *testing.T) {
	ag := store.ProbeAgent{Procs: store.ProbeProcs{
		CPU:  store.ProbeCPU{Count: 2, Peak: 1.3, Starved: true},
		Held: map[string]store.ProcHold{"StartPingers": {From: 5, To: 8}},
	}}
	title, detail := probeCPUNotice("site1", ag)
	if title != "Probe site1 may be short on CPU" || !strings.Contains(detail, "icmp pingers") || !strings.Contains(detail, "Docker host") {
		t.Fatalf("container: %q / %q", title, detail)
	}
	ag.OSReportedAt = 1
	if _, detail := probeCPUNotice("site1", ag); !strings.Contains(detail, "more vCPUs") {
		t.Fatalf("VM: %q", detail)
	}
}
