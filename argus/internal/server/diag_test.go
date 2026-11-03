// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/diag"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A queued run rides the probe's next check-in once; the probe's results come back to it, and
// another probe can't report on it.
func TestDiagRidesTheCheckin(t *testing.T) {
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
	s := &Server{st: st, mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), diags: newDiagBoard()}
	for name, tok := range map[string]string{"proxy-site1": "tok-1", "proxy-site2": "tok-2"} {
		if err := st.UpsertProbeCredential(ctx, name, auth.HashToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	checkin := func() map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(`{"version":"7.0.32-r14","scans":true,"sweeps":true,"diags":true}`))
		r.Header.Set("Authorization", "Bearer tok-1")
		w := httptest.NewRecorder()
		s.handleProbeCheckin(w, r)
		if w.Code != 200 {
			t.Fatalf("check-in: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if out := checkin(); out["diag"] != nil || out["next"] != nil {
		t.Fatalf("nothing queued: want the usual minute, got %v", out)
	}
	if ag, err := st.ProbeAgentByName(ctx, "proxy-site1"); err != nil || !ag.Diags {
		t.Fatalf("the diagnostics capability was not kept: %+v %v", ag, err)
	}
	now := time.Now()
	run := &diagRun{Source: "proxy-site1", SourceName: "proxy-site1", Req: diag.Request{Tool: "ping", Target: "10.0.0.20", Count: 5},
		State: "queued", By: 1, Created: now, Updated: now}
	if !s.diags.add(run) {
		t.Fatal("could not queue")
	}
	out := checkin()
	job, _ := out["diag"].(map[string]any)
	if job == nil || job["tool"] != "ping" || job["target"] != "10.0.0.20" || job["id"] != float64(run.ID) {
		t.Fatalf("queued: want the job, got %v", out)
	}
	if out := checkin(); out["diag"] != nil || out["next"] != nil {
		t.Fatalf("handed out: want it once, got %v", out)
	}
	if got, _ := s.diags.get(run.ID); got.State != "sent" {
		t.Fatalf("handed out: want sent until its first result, got %s", got.State)
	}
	report := func(tok, body string) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/diag-results", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.handleDiagResults(w, r)
		return w.Code
	}
	id := strconv.FormatInt(run.ID, 10)
	if code := report("tok-2", `{"id":`+id+`,"result":{"done":true,"raw":"x"}}`); code != 404 {
		t.Fatalf("another probe's report: want 404, got %d", code)
	}
	if code := report("tok-1", `{"id":`+id+`,"result":{"raw":"PING","ping":{"replies":[{"seq":0,"from":"10.0.0.20","ms":0.4}]}}}`); code != 204 {
		t.Fatalf("partial report: %d", code)
	}
	if got, _ := s.diags.get(run.ID); got.State != "running" || len(got.Result.Ping.Replies) != 1 {
		t.Fatalf("partial: %+v", got)
	}
	if code := report("tok-1", `{"id":`+id+`,"result":{"done":true,"raw":"PING\n--- stats","ping":{"replies":[{"seq":0,"ms":0.4},{"seq":1}]}}}`); code != 204 {
		t.Fatalf("last report: %d", code)
	}
	if got, _ := s.diags.get(run.ID); got.State != "done" || !got.Result.Done || got.Result.Raw != "PING\n--- stats" {
		t.Fatalf("done: %+v", got)
	}
	if code := report("tok-1", `{"id":`+id+`,"result":{"done":true}}`); code != 404 {
		t.Fatalf("a finished run takes no more reports, got %d", code)
	}
}

// One run at a time per source; a run the probe never takes fails with why; an open dialog warms
// its probe.
func TestDiagBoard(t *testing.T) {
	b := newDiagBoard()
	old := time.Now().Add(-2 * diagPickupWait)
	first := &diagRun{Source: "proxy-site1", SourceName: "proxy-site1", State: "queued", Created: old, Updated: old}
	if !b.add(first) {
		t.Fatal("first run refused")
	}
	if b.add(&diagRun{Source: "proxy-site1", State: "queued", Created: old, Updated: old}) {
		t.Fatal("a second run from the same probe was taken")
	}
	if !b.add(&diagRun{Source: "core", State: "running", Created: time.Now(), Updated: time.Now()}) {
		t.Fatal("the core is another source")
	}
	got, _ := b.get(first.ID)
	if got.State != "failed" || !strings.Contains(got.Result.Error, "did not pick it up") {
		t.Fatalf("an old queued run: %+v", got)
	}
	if !b.add(&diagRun{Source: "proxy-site1", State: "queued", Created: time.Now(), Updated: time.Now()}) {
		t.Fatal("the failed run still blocks its probe")
	}
	if !b.fast("proxy-site1") {
		t.Fatal("a probe with a run waiting for it does not check in fast")
	}
	if b.fast("proxy-site2") {
		t.Fatal("a cold probe with nothing queued checks in fast")
	}
	b.warmUp("proxy-site2")
	if !b.fast("proxy-site2") {
		t.Fatal("a warmed probe does not check in fast")
	}
	// A probe that took a run but whose results never arrive: failed, saying so, not "stopped reporting".
	silent := time.Now().Add(-2 * diagSilentAfter)
	took := &diagRun{Source: "proxy-site3", SourceName: "proxy-site3", State: "sent", Created: silent, Updated: silent}
	b.add(took)
	if got, _ := b.get(took.ID); got.State != "failed" || !strings.Contains(got.Result.Error, "none of its results reached Argus") {
		t.Fatalf("a sent run with no word back: %+v", got)
	}
}

// What a probe reports is bounded and loses its control characters, except the raw output's lines.
func TestCleanDiagResult(t *testing.T) {
	long := strings.Repeat("h", 400)
	res := cleanDiagResult(diag.Result{
		Error: "bad\x1b[31m\nthing", Raw: "line 1\nline\x072\ttab",
		Trace: &diag.Trace{Hops: make([]diag.Hop, 40)},
		DNS:   &diag.DNS{Answers: []diag.Answer{{Type: "A", Value: long}}},
	})
	if res.Error != "bad[31mthing" || res.Raw != "line 1\nline2\ttab" || len(res.Trace.Hops) != diag.MaxHops || len(res.DNS.Answers[0].Value) != 400 {
		t.Fatalf("%+v", res)
	}
}
