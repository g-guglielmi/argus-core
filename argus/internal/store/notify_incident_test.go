// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// The notifier's incident start survives a save and a later update (the firing upsert rewrites the
// row), while first_seen - the flap-debounce anchor - keeps its original value.
func TestNotifyIncidentStart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertNotifyState(ctx, NotifyState{EventID: "e1", HostID: "50", ItemID: "900", Name: "Probe unreachable", Severity: 4, State: "pending", FirstSeen: 1000, IncidentStart: 700}); err != nil {
		t.Fatal(err)
	}
	fired := int64(1060)
	if err := st.UpsertNotifyState(ctx, NotifyState{EventID: "e1", HostID: "50", ItemID: "900", Name: "Probe unreachable", Severity: 4, State: "firing", FirstSeen: 9999, FiredAt: &fired, IncidentStart: 640}); err != nil {
		t.Fatal(err)
	}
	states, err := st.NotifyStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := states["e1"]
	if got.IncidentStart != 640 || got.FirstSeen != 1000 || got.State != "firing" {
		t.Fatalf("state after update: %+v", got)
	}
}
