package store

import (
	"context"
	"testing"
)

// TestItemPriority covers the PRTG-style per-sensor priority store: overrides round-trip, and setting
// a sensor back to the default clears its row (so the table only holds real overrides).
func TestItemPriority(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// No overrides yet.
	m, err := st.ItemPriorities(ctx)
	if err != nil {
		t.Fatalf("ItemPriorities: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected no overrides, got %v", m)
	}

	// Set two overrides; a later write updates in place.
	if err := st.SetItemPriority(ctx, "item-a", 5, 1); err != nil {
		t.Fatalf("SetItemPriority a: %v", err)
	}
	if err := st.SetItemPriority(ctx, "item-b", 1, 1); err != nil {
		t.Fatalf("SetItemPriority b: %v", err)
	}
	if err := st.SetItemPriority(ctx, "item-a", 4, 2); err != nil {
		t.Fatalf("SetItemPriority a update: %v", err)
	}
	m, err = st.ItemPriorities(ctx)
	if err != nil {
		t.Fatalf("ItemPriorities: %v", err)
	}
	if m["item-a"] != 4 || m["item-b"] != 1 {
		t.Fatalf("expected a=4 b=1, got %v", m)
	}

	// Setting back to the default removes the row (keeps the table sparse).
	if err := st.SetItemPriority(ctx, "item-a", DefaultItemPriority, 1); err != nil {
		t.Fatalf("SetItemPriority a default: %v", err)
	}
	m, err = st.ItemPriorities(ctx)
	if err != nil {
		t.Fatalf("ItemPriorities: %v", err)
	}
	if _, ok := m["item-a"]; ok {
		t.Fatalf("expected item-a cleared at default, got %v", m)
	}
	if m["item-b"] != 1 {
		t.Fatalf("expected b=1 to remain, got %v", m)
	}
}
