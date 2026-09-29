// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
	"time"
)

func TestProbeProcsLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", "hash"); err != nil {
		t.Fatal(err)
	}
	get := func() ProbeProcs {
		t.Helper()
		a, err := st.ProbeAgentByName(ctx, "proxy-site1")
		if err != nil {
			t.Fatal(err)
		}
		return a.Procs
	}

	// First report: the target is seeded with what runs, minus what the operator pinned.
	if err := st.RecordProbeProcs(ctx, "proxy-site1", map[string]int{"StartPingers": 5, "StartPollers": 5}, []string{"StartPollers"}); err != nil {
		t.Fatal(err)
	}
	p := get()
	if p.Target["StartPingers"] != 5 || len(p.Target) != 1 || p.Since == 0 || p.Pending() {
		t.Fatalf("seeded: %+v", p)
	}
	since := p.Since

	// Same counts again: the settle clock keeps running.
	time.Sleep(1100 * time.Millisecond)
	if err := st.RecordProbeProcs(ctx, "proxy-site1", map[string]int{"StartPingers": 5, "StartPollers": 5}, []string{"StartPollers"}); err != nil {
		t.Fatal(err)
	}
	if get().Since != since {
		t.Fatal("unchanged counts must not reset the settle clock")
	}

	// Argus raises the pingers: pending until the probe reports running them.
	if err := st.SetProbeProcsEvaluation(ctx, "proxy-site1", map[string]float64{"StartPingers": 72}, map[string]int{"StartPingers": 8}, "ICMP pingers 5 -> 8", true); err != nil {
		t.Fatal(err)
	}
	p = get()
	if !p.Pending() || p.Note == "" || p.DecidedAt == 0 || p.Peaks["StartPingers"] != 72 {
		t.Fatalf("after decision: %+v", p)
	}
	if err := st.RecordProbeProcs(ctx, "proxy-site1", map[string]int{"StartPingers": 8, "StartPollers": 5}, []string{"StartPollers"}); err != nil {
		t.Fatal(err)
	}
	p = get()
	if p.Pending() || p.Since == since {
		t.Fatalf("applied: pending=%v since=%d (was %d)", p.Pending(), p.Since, since)
	}

	// A restart is claimed once per gap.
	if ok, err := st.ClaimProbeRestart(ctx, "proxy-site1", time.Hour); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := st.ClaimProbeRestart(ctx, "proxy-site1", time.Hour); ok {
		t.Fatal("second claim inside the gap must fail")
	}
}
