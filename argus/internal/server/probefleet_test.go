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
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A sidecar asked for both a proxy and its own update gets them one check-in apart, the proxy first:
// its own update replaces it, so the two never run side by side.
func TestCheckinHandsOutOneUpdate(t *testing.T) {
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
	// Known digests, so the hand-outs don't ask the registry.
	s.digests.entries = map[string]digestEntry{}
	for _, k := range []string{probeImageRepo + ":latest", updaterImageRepo + ":latest"} {
		s.digests.entries[k] = digestEntry{digest: "sha256:" + strings.Repeat("a", 64), at: time.Now()}
	}
	const raw = "probe-token"
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	checkin := func() map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(`{"selfupdate":true,"updater_version":"v0.2.11"}`))
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
	checkin() // the probe is known from here on
	if err := st.SetProbeUpdate(ctx, "proxy-site1", "latest"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpdaterUpdate(ctx, "proxy-site1", "latest"); err != nil {
		t.Fatal(err)
	}
	if out := checkin(); out["update"] != "latest" || out["updater_update"] != nil {
		t.Fatalf("first check-in: want the proxy update alone, got %v", out)
	}
	if out := checkin(); out["update"] != nil || out["updater_update"] != "latest" {
		t.Fatalf("second check-in: want the sidecar update, got %v", out)
	}
	if out := checkin(); out["update"] != nil || out["updater_update"] != nil {
		t.Fatalf("third check-in: want nothing left, got %v", out)
	}
}

// updateStatus must never propose a downgrade: a probe at or ahead of the GHCR-resolved latest is
// current (the latest cache lags a just-published release), while a genuinely older probe is outdated.
// An explicit pin is a deliberate target, so any mismatch converges to it.
func TestUpdateStatus(t *testing.T) {
	cases := []struct {
		name                     string
		reported, target, latest string
		want                     string
	}{
		{"no version yet", "", "latest", "7.0.30-r9", "unknown"},
		{"latest not resolved", "7.0.30-r9", "latest", "", "tracking"},
		{"equal to latest", "7.0.30-r9", "latest", "7.0.30-r9", "current"},
		{"behind latest", "7.0.30-r8", "latest", "7.0.30-r9", "outdated"},
		// The reported case: fleet already on r10 while the GHCR cache still says r9 - must NOT be
		// flagged outdated (that would offer a downgrade to r9).
		{"ahead of stale latest", "7.0.30-r10", "latest", "7.0.30-r9", "current"},
		{"ahead by patch", "7.0.31-r1", "latest", "7.0.30-r9", "current"},
		// Explicit pins are deliberate: match is current, anything else (older or newer) converges.
		{"pin matched", "7.0.30-r9", "7.0.30-r9", "7.0.30-r10", "current"},
		{"pin older than running", "7.0.30-r10", "7.0.29-r1", "7.0.30-r10", "outdated"},
		{"pin newer than running", "7.0.30-r8", "7.0.30-r9", "7.0.30-r9", "outdated"},
	}
	for _, c := range cases {
		if got := updateStatus(c.reported, c.target, c.latest); got != c.want {
			t.Errorf("%s: updateStatus(%q,%q,%q)=%q, want %q", c.name, c.reported, c.target, c.latest, got, c.want)
		}
	}
}
