// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"testing"
)

func TestMaintenanceWindowsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.CreateMaintenanceWindow(ctx, MaintenanceWindow{Name: "Backups", Sites: []string{"site1"}, HostIDs: []string{"101"},
		Kind: "weekly", Weekdays: 1, Minute: 120, DurationMin: 120, Enabled: true, CreatedBy: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.MaintenanceWindow(ctx, id)
	if err != nil || w.Name != "Backups" || len(w.Sites) != 1 || w.HostIDs[0] != "101" || !w.Enabled || w.CreatedAt == 0 {
		t.Fatalf("read back %+v %v", w, err)
	}
	w.Name, w.Enabled, w.HostIDs = "Nightly backups", false, nil
	if err := st.UpdateMaintenanceWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	all, _ := st.MaintenanceWindows(ctx)
	if len(all) != 1 || all[0].Name != "Nightly backups" || all[0].Enabled || len(all[0].HostIDs) != 0 || all[0].CreatedBy != "ops@example.com" {
		t.Fatalf("after update %+v", all)
	}
	if err := st.UpdateMaintenanceWindow(ctx, MaintenanceWindow{ID: 999, Name: "x", Kind: "daily", DurationMin: 5}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updating a missing window: %v", err)
	}
	_ = st.DeleteMaintenanceWindow(ctx, id)
	if _, err := st.MaintenanceWindow(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted window still there: %v", err)
	}
}
