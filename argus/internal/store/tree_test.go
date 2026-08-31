package store

import (
	"context"
	"testing"
)

// TestTreeOrder covers the manual tree-order store: round-trip, per-(scope,kind) isolation, replace
// semantics, clearing, and rejection of an unknown kind.
func TestTreeOrder(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if sets, err := st.TreeOrder(ctx); err != nil || len(sets) != 0 {
		t.Fatalf("expected empty order, got %v err=%v", sets, err)
	}

	// Two sibling sets under different scopes/kinds.
	if err := st.SetTreeOrder(ctx, "", "group", []string{"site1/Network", "site1/Infrastructure"}); err != nil {
		t.Fatalf("set group order: %v", err)
	}
	if err := st.SetTreeOrder(ctx, "site1/Network", "host", []string{"h2", "h1"}); err != nil {
		t.Fatalf("set host order: %v", err)
	}

	sets, err := st.TreeOrder(ctx)
	if err != nil {
		t.Fatalf("TreeOrder: %v", err)
	}
	byKey := map[string][]string{}
	for _, s := range sets {
		byKey[s.Scope+"|"+s.Kind] = s.Items
	}
	if got := byKey["|group"]; len(got) != 2 || got[0] != "site1/Network" || got[1] != "site1/Infrastructure" {
		t.Fatalf("group order round-trip mismatch: %v", got)
	}
	if got := byKey["site1/Network|host"]; len(got) != 2 || got[0] != "h2" || got[1] != "h1" {
		t.Fatalf("host order round-trip mismatch: %v", got)
	}

	// Replace fully overwrites the prior order for that set only.
	if err := st.SetTreeOrder(ctx, "", "group", []string{"site1/Infrastructure", "site1/Network"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	sets, _ = st.TreeOrder(ctx)
	for _, s := range sets {
		if s.Scope == "" && s.Kind == "group" && (len(s.Items) != 2 || s.Items[0] != "site1/Infrastructure") {
			t.Fatalf("replace did not overwrite: %v", s.Items)
		}
		if s.Scope == "site1/Network" && s.Kind == "host" && len(s.Items) != 2 {
			t.Fatalf("replace bled into another set: %v", s.Items)
		}
	}

	// Empty items clears a set.
	if err := st.SetTreeOrder(ctx, "", "group", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	sets, _ = st.TreeOrder(ctx)
	for _, s := range sets {
		if s.Scope == "" && s.Kind == "group" {
			t.Fatalf("expected group set cleared, still present: %v", s.Items)
		}
	}

	// Unknown kind is rejected.
	if err := st.SetTreeOrder(ctx, "", "sensor", []string{"x"}); err == nil {
		t.Fatalf("expected error for invalid kind")
	}
}

// TestHiddenGroups covers hide/unhide of tree groups, including idempotent hide and unhide of an
// absent path.
func TestHiddenGroups(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if h, err := st.HiddenGroups(ctx); err != nil || len(h) != 0 {
		t.Fatalf("expected none hidden, got %v err=%v", h, err)
	}
	if err := st.SetGroupHidden(ctx, "Applications", true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	if err := st.SetGroupHidden(ctx, "Applications", true); err != nil { // idempotent
		t.Fatalf("re-hide: %v", err)
	}
	if err := st.SetGroupHidden(ctx, "Databases", true); err != nil {
		t.Fatalf("hide 2: %v", err)
	}
	h, err := st.HiddenGroups(ctx)
	if err != nil || len(h) != 2 || h[0] != "Applications" || h[1] != "Databases" {
		t.Fatalf("hidden set mismatch: %v err=%v", h, err)
	}
	if err := st.SetGroupHidden(ctx, "Applications", false); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	if err := st.SetGroupHidden(ctx, "Nonexistent", false); err != nil { // unhide of absent is a no-op
		t.Fatalf("unhide absent: %v", err)
	}
	if h, _ = st.HiddenGroups(ctx); len(h) != 1 || h[0] != "Databases" {
		t.Fatalf("after unhide mismatch: %v", h)
	}
}
