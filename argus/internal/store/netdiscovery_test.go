// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"testing"
)

// The full job lifecycle: queue -> single-flight -> one-shot handout at check-in -> results with
// the ignored state carried over from the previous scan of the same probe.
func TestDiscoveryJobLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{
		ProxyName: "proxy-site1", CIDR: "10.0.0.0/24",
		SNMPVersion: 2, SNMPCommunity: "public", SNMPPort: 161, RequestedBy: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Single flight per probe; a second probe is unaffected.
	if _, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-site1", CIDR: "10.0.1.0/24"}); !errors.Is(err, ErrDiscoveryBusy) {
		t.Fatalf("second job for the same probe: err = %v, want ErrDiscoveryBusy", err)
	}
	if _, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-site2", CIDR: "10.0.2.0/24"}); err != nil {
		t.Fatalf("job for another probe should be fine: %v", err)
	}

	// Handout is one-shot and decrypts the community; the wrong probe gets nothing.
	if j, _ := st.TakeDiscoveryJob(ctx, "proxy-other"); j != nil {
		t.Fatal("a different probe must not receive this job")
	}
	j, err := st.TakeDiscoveryJob(ctx, "proxy-site1")
	if err != nil || j == nil {
		t.Fatalf("take: %v, job=%v", err, j)
	}
	if j.ID != id || j.CIDR != "10.0.0.0/24" || j.SNMPCommunity != "public" || j.State != "dispatched" {
		t.Fatalf("handout mismatch: %+v", j)
	}
	if j2, _ := st.TakeDiscoveryJob(ctx, "proxy-site1"); j2 != nil {
		t.Fatal("the job must be handed out exactly once")
	}

	// Completion by the wrong probe is rejected without leaking existence.
	if err := st.CompleteDiscoveryJob(ctx, id, "proxy-other", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-probe completion: err = %v, want ErrNotFound", err)
	}
	results := []DiscoveryResult{
		{IP: "10.0.0.5", RDNS: "gw.example.lan", TCPPorts: "[80,443]", SuggestedClass: "unifi-gateway"},
		{IP: "10.0.0.9", TCPPorts: "[22]", SuggestedClass: "linux-ssh"},
	}
	if err := st.CompleteDiscoveryJob(ctx, id, "proxy-site1", "", results); err != nil {
		t.Fatal(err)
	}
	got, err := st.DiscoveryJobByID(ctx, id)
	if err != nil || got.State != "done" {
		t.Fatalf("job after completion: %+v, err=%v", got, err)
	}
	rows, err := st.DiscoveryResultsByJob(ctx, id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("results: %v, err=%v", rows, err)
	}
	if rows[0].State != "new" || rows[0].SuggestedClass != "unifi-gateway" {
		t.Fatalf("row 0: %+v", rows[0])
	}

	// Ignore 10.0.0.9, adopt 10.0.0.5.
	if err := st.SetDiscoveryResultsState(ctx, []int64{rows[1].ID}, "ignored"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDiscoveryResultAdded(ctx, rows[0].ID, "10105"); err != nil {
		t.Fatal(err)
	}
	// An adopted row can't be flipped back.
	_ = st.SetDiscoveryResultsState(ctx, []int64{rows[0].ID}, "ignored")
	rows, _ = st.DiscoveryResultsByJob(ctx, id)
	if rows[0].State != "added" || rows[0].HostID != "10105" {
		t.Fatalf("adopted row: %+v", rows[0])
	}
	if rows[1].State != "ignored" {
		t.Fatalf("ignored row: %+v", rows[1])
	}

	// A re-scan of the same probe carries the ignored state over by IP; the adopted IP is 'new'
	// again (the live monitored-IP check annotates it instead).
	id2, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-site1", CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := st.TakeDiscoveryJob(ctx, "proxy-site1"); j == nil {
		t.Fatal("second scan should be handed out")
	}
	if err := st.CompleteDiscoveryJob(ctx, id2, "proxy-site1", "", []DiscoveryResult{
		{IP: "10.0.0.5", TCPPorts: "[443]"}, {IP: "10.0.0.9", TCPPorts: "[22]"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.DiscoveryResultsByJob(ctx, id2)
	if rows[0].State != "new" {
		t.Fatalf("re-scan of an adopted IP: state = %q, want new", rows[0].State)
	}
	if rows[1].State != "ignored" {
		t.Fatalf("re-scan of an ignored IP: state = %q, want ignored (carry-over)", rows[1].State)
	}
}

// A failed scan (error, no hosts) fails the job; an error WITH hosts is a partial done.
func TestDiscoveryJobFailureAndPartial(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, _ := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-a", CIDR: "10.0.0.0/24"})
	if j, _ := st.TakeDiscoveryJob(ctx, "proxy-a"); j == nil {
		t.Fatal("handout failed")
	}
	if err := st.CompleteDiscoveryJob(ctx, id, "proxy-a", "bad cidr", nil); err != nil {
		t.Fatal(err)
	}
	j, _ := st.DiscoveryJobByID(ctx, id)
	if j.State != "failed" || j.Error != "bad cidr" {
		t.Fatalf("failed job: %+v", j)
	}

	id2, _ := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-a", CIDR: "10.0.0.0/24"})
	if j, _ := st.TakeDiscoveryJob(ctx, "proxy-a"); j == nil {
		t.Fatal("handout failed")
	}
	if err := st.CompleteDiscoveryJob(ctx, id2, "proxy-a", "partial", []DiscoveryResult{{IP: "10.0.0.1", TCPPorts: "[]"}}); err != nil {
		t.Fatal(err)
	}
	j, _ = st.DiscoveryJobByID(ctx, id2)
	if j.State != "done" || j.Error != "partial" {
		t.Fatalf("partial job: %+v", j)
	}
	// A done job rejects a second completion.
	if err := st.CompleteDiscoveryJob(ctx, id2, "proxy-a", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double completion: err = %v, want ErrNotFound", err)
	}
}
