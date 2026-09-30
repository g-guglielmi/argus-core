// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"path/filepath"
	"testing"
)

// The log opens an incident once, keeps the reason it opened with, closes it when it is no longer
// open, and opens a new one if it comes back.
func TestSyncArgusIncidents(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	a := OpenArgusIncident{EventID: "argus-unsupported-1", HostID: "10", HostName: "gw", ItemID: "1",
		Name: "CPU stopped collecting", Severity: 4, Reason: "UniFi API HTTP 400", StartedAt: 1000}
	b := OpenArgusIncident{EventID: "argus-interface-7", HostID: "11", HostName: "nas", Name: "SNMP not responding", Severity: 4, StartedAt: 1100}

	if err := st.SyncArgusIncidents(ctx, []OpenArgusIncident{a, b}, 1200); err != nil {
		t.Fatal(err)
	}
	a2 := a
	a2.Reason = "something else"
	if err := st.SyncArgusIncidents(ctx, []OpenArgusIncident{a2}, 1300); err != nil { // b closed
		t.Fatal(err)
	}
	if err := st.SyncArgusIncidents(ctx, []OpenArgusIncident{a2, b}, 1400); err != nil { // b again
		t.Fatal(err)
	}
	got, err := st.ArgusIncidents(ctx, nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows (a, b closed, b reopened), got %+v", got)
	}
	var open, closed int
	for _, g := range got {
		if g.EventID == a.EventID && (g.Reason != "UniFi API HTTP 400" || g.EndedAt != 0 || g.StartedAt != 1000) {
			t.Fatalf("a must keep its first reason and stay open: %+v", g)
		}
		if g.EventID == b.EventID {
			if g.EndedAt == 0 {
				open++
			} else if g.EndedAt == 1300 {
				closed++
			}
		}
	}
	if open != 1 || closed != 1 {
		t.Fatalf("b: want one closed at 1300 and one open, got open=%d closed=%d", open, closed)
	}
	if only, _ := st.ArgusIncidents(ctx, []string{"11"}, 0, 10); len(only) != 2 {
		t.Fatalf("host filter: want 2 rows for host 11, got %d", len(only))
	}
	if err := st.PruneArgusIncidents(ctx, 1350); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.ArgusIncidents(ctx, nil, 0, 10); len(left) != 2 {
		t.Fatalf("prune must drop only the closed one: %d left", len(left))
	}
}

func TestUptimeDaysStore(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if err := st.PutUptimeDays(ctx, []UptimeDay{{"1", "2026-09-01", 1440, 1440}, {"1", "2026-09-02", 700, 1440}, {"2", "2026-09-02", 0, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUptimeDays(ctx, []UptimeDay{{"1", "2026-09-02", 720, 1440}}); err != nil { // rewrite
		t.Fatal(err)
	}
	got, err := st.UptimeDays(ctx, []string{"1", "2"}, "2026-09-02")
	if err != nil || len(got) != 2 {
		t.Fatalf("want 2 rows from 09-02, got %v %v", got, err)
	}
	for _, d := range got {
		if d.ItemID == "1" && d.Up != 720 {
			t.Fatalf("rewrite lost: %+v", d)
		}
	}
	if err := st.PruneUptimeDays(ctx, "2026-09-02"); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.UptimeDays(ctx, []string{"1", "2"}, "2000-01-01"); len(all) != 2 {
		t.Fatalf("prune: want 2 left, got %d", len(all))
	}
}
