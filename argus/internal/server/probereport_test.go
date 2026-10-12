// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestReportStale(t *testing.T) {
	const now = int64(1_800_000_000)
	min := func(m int64) int64 { return now - m*60 }
	started := min(60) // Argus up for an hour
	cases := []struct {
		name   string
		ag     store.ProbeAgent
		online bool
		start  int64
		want   bool
	}{
		{"reporting", store.ProbeAgent{CredentialAt: min(600), ReportAt: min(1)}, true, started, false},
		{"report stopped", store.ProbeAgent{CredentialAt: min(600), ReportAt: min(6)}, true, started, true},
		{"just under the grace", store.ProbeAgent{CredentialAt: min(600), ReportAt: now - 299}, true, started, false},
		{"never reported (r8 to r28)", store.ProbeAgent{CredentialAt: min(600)}, true, started, true},
		{"just enrolled", store.ProbeAgent{CredentialAt: min(2)}, true, started, false},
		{"Argus just started", store.ProbeAgent{CredentialAt: min(600)}, true, min(2), false},
		{"Zabbix doesn't hear from it", store.ProbeAgent{CredentialAt: min(600), ReportAt: min(30)}, false, started, false},
		{"no check-in credential", store.ProbeAgent{}, true, started, false},
	}
	for _, c := range cases {
		if got := reportStale(c.ag, c.online, c.start, now); got != c.want {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReportStaleDetail(t *testing.T) {
	loc := time.UTC
	at := time.Date(2026, 10, 12, 9, 15, 0, 0, loc).Unix()
	if d := reportStaleDetail(store.ProbeAgent{ReportAt: at}, loc); !strings.Contains(d, "since Oct 12, 09:15.") {
		t.Errorf("detail with a last report: %q", d)
	}
	if d := reportStaleDetail(store.ProbeAgent{}, loc); !strings.Contains(d, "has never reached Argus") {
		t.Errorf("detail without one: %q", d)
	}
}

// Only the probe container's minute report (the check-in carrying the scan capability) counts as a
// report: the start-time sync, the updater sidecar's check-ins and an unreadable body don't.
func TestCheckinReportAt(t *testing.T) {
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
	checkin := func(body string) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleProbeCheckin(w, r)
		return w.Code
	}
	reportAt := func() int64 {
		t.Helper()
		ag, err := st.ProbeAgentByName(ctx, "proxy-site1")
		if err != nil {
			t.Fatal(err)
		}
		if ag.CredentialAt == 0 {
			t.Fatal("no credential time")
		}
		return ag.ReportAt
	}
	if code := checkin(``); code != 400 { // what r8 to r28 sent every minute
		t.Fatalf("empty body: %d, want 400", code)
	}
	for _, body := range []string{
		`{"version":"7.0.31-r29"}`, // the start-time sync
		`{"selfupdate":true,"updater_version":"v0.2.11","restarts":true}`, // the sidecar
	} {
		if code := checkin(body); code != 200 {
			t.Fatalf("%s: %d", body, code)
		}
		if at := reportAt(); at != 0 {
			t.Fatalf("%s set report_at %d", body, at)
		}
	}
	if code := checkin(`{"version":"7.0.31-r29","scans":true,"sweeps":true,"procs":null,"procs_pinned":[],"cpu":null}`); code != 200 {
		t.Fatalf("minute report: %d", code)
	}
	if at := reportAt(); time.Since(time.Unix(at, 0)) > time.Minute {
		t.Fatalf("minute report left report_at at %d", at)
	}
	// A re-enrollment starts over.
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	if at := reportAt(); at != 0 {
		t.Fatalf("re-enrollment kept report_at %d", at)
	}
}
