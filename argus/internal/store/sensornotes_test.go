// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// One note shows per sensor: writing again replaces its text (and restarts its OK countdown);
// clearing it keeps it for the history, where the next one starts afresh.
func TestSensorNotes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	n, err := st.SetSensorNote(ctx, "1001", "10", "ISP ticket open", 1, "Alice Rossi", 100)
	if err != nil || n.Text != "ISP ticket open" || n.ByName != "Alice Rossi" || n.CreatedAt != 100 {
		t.Fatalf("set: %+v %v", n, err)
	}
	if err := st.SetSensorNoteOKSince(ctx, n.ID, 150); err != nil {
		t.Fatal(err)
	}
	n2, err := st.SetSensorNote(ctx, "1001", "10", "Technician on site at 14:00", 2, "Bob", 200)
	if err != nil || n2.ID != n.ID || n2.Text != "Technician on site at 14:00" || n2.CreatedAt != 100 || n2.UpdatedAt != 200 || n2.OKSince != 0 {
		t.Fatalf("rewrite: %+v %v", n2, err)
	}
	if _, err := st.SetSensorNote(ctx, "argus-interface-7", "11", "Switch replaced", 1, "Alice Rossi", 210); err != nil {
		t.Fatal(err)
	}
	live, err := st.LiveSensorNotes(ctx)
	if err != nil || len(live) != 2 || live["1001"].Text != "Technician on site at 14:00" {
		t.Fatalf("live: %+v %v", live, err)
	}

	if err := st.ClearSensorNote(ctx, "1001", 300); err != nil {
		t.Fatal(err)
	}
	if live, _ := st.LiveSensorNotes(ctx); len(live) != 1 {
		t.Fatalf("cleared note still shown: %+v", live)
	}
	n3, err := st.SetSensorNote(ctx, "1001", "10", "Again", 1, "Alice Rossi", 400)
	if err != nil || n3.ID == n.ID || n3.CreatedAt != 400 {
		t.Fatalf("a new incident's note: %+v %v", n3, err)
	}

	hist, err := st.SensorNotesSince(ctx, []string{"1001"}, 250)
	if err != nil || len(hist) != 2 || hist[0].Text != "Technician on site at 14:00" || hist[0].ClearedAt != 300 || hist[1].Text != "Again" {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if hist, _ := st.SensorNotesSince(ctx, []string{"1001"}, 350); len(hist) != 1 {
		t.Fatalf("a note cleared before `from` is in the history: %+v", hist)
	}

	if err := st.PruneSensorNotes(ctx, 350); err != nil {
		t.Fatal(err)
	}
	if hist, _ := st.SensorNotesSince(ctx, []string{"1001"}, 0); len(hist) != 1 {
		t.Fatalf("prune kept an old cleared note: %+v", hist)
	}
}
