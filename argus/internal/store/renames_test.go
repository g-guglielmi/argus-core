// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// Linking a renamed sensor hands the new one what Argus kept about the old one; what the new one has
// of its own stays.
func TestMoveSensor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetItemPriority(ctx, "old", 5, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRunbook(ctx, Runbook{Scope: "item", ItemID: "old", Text: "old runbook"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRunbook(ctx, Runbook{Scope: "item", ItemID: "new", Text: "new runbook"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateReport(ctx, Report{Name: "r", Spec: ReportSpec{Sensors: []ReportSensor{{HostID: "h", Items: []string{"x", "old"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUptimeDays(ctx, []UptimeDay{{ItemID: "new", Day: "2026-11-01", Up: 0, Num: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveSensor(ctx, "old", "new"); err != nil {
		t.Fatal(err)
	}
	pr, _ := st.ItemPriorities(ctx)
	if pr["new"] != 5 {
		t.Fatalf("priority not moved: %v", pr)
	}
	rbs, _ := st.Runbooks(ctx)
	for _, rb := range rbs {
		if rb.ItemID == "new" && rb.Text != "new runbook" {
			t.Fatalf("the new sensor's own runbook was replaced: %+v", rb)
		}
	}
	reps, _ := st.Reports(ctx)
	if len(reps) != 1 || reps[0].Spec.Sensors[0].Items[1] != "new" {
		t.Fatalf("report entry not moved: %+v", reps)
	}
	if d, _ := st.UptimeDays(ctx, []string{"new"}, "2026-01-01"); len(d) != 0 {
		t.Fatalf("stale uptime days kept: %v", d)
	}
}

// Kept readings come back by sensor and time; pruning drops the old ones and the links left empty.
func TestSensorArchive(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.PutSensorArchive(ctx, []ArchPoint{{"a", 10, 1}, {"a", 20, 0}, {"a", 20, 5}, {"b", 15, 3}},
		[]ArchTrend{{"a", 3600, 0, 0.5, 1, 60}}); err != nil {
		t.Fatal(err)
	}
	h, _ := st.ArchivedHistory(ctx, []string{"a"}, 0, 100)
	if len(h) != 2 || h[1].V != 0 {
		t.Fatalf("history: %v", h)
	}
	tr, _ := st.ArchivedTrends(ctx, []string{"a", "b"}, 0, 10000)
	if len(tr) != 1 || tr[0].Num != 60 {
		t.Fatalf("trends: %v", tr)
	}
	if err := st.PutSensorRename(ctx, SensorRename{OldID: "b", NewID: "c", HostID: "h", At: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneSensorArchive(ctx, 18, 5000); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.ArchivedHistory(ctx, []string{"a", "b"}, 0, 100); len(h) != 1 || h[0].Clock != 20 {
		t.Fatalf("after prune: %v", h)
	}
	if rs, _ := st.SensorRenames(ctx); len(rs) != 0 {
		t.Fatalf("an empty link kept: %v", rs)
	}
}

// An old sensor's incidents are kept once, as kept ones, on the new sensor.
func TestArchivedIncidents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	in := []ArgusIncident{{EventID: "77", HostID: "h", HostName: "web1", ItemID: "new", Name: "Service down", Severity: 4, StartedAt: 100, EndedAt: 200}}
	for range 2 {
		if err := st.PutArchivedIncidents(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.ArgusIncidents(ctx, nil, 0, 10)
	if len(got) != 1 || got[0].Kind != "renamed" || got[0].ItemID != "new" {
		t.Fatalf("got %+v", got)
	}
}
