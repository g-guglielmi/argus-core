package store

import (
	"context"
	"testing"
)

// TestSNMPDefaults covers the proxy SNMP-default store (round-trip incl. the v3 secret fields, upsert)
// and the per-interface inherit flags.
func TestSNMPDefaults(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := st.SNMPDefaultFor(ctx, "p1"); err != nil || ok {
		t.Fatalf("expected no default, got ok=%v err=%v", ok, err)
	}

	d := SNMPDefault{Version: 3, SecName: "user", SecLevel: 2, AuthProto: 1, AuthPass: "authpw", PrivProto: 3, PrivPass: "privpw", ContextName: "ctx"}
	if err := st.SetSNMPDefault(ctx, "p1", d); err != nil {
		t.Fatalf("SetSNMPDefault: %v", err)
	}
	got, ok, err := st.SNMPDefaultFor(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("SNMPDefaultFor: ok=%v err=%v", ok, err)
	}
	if got.Version != 3 || got.SecName != "user" || got.SecLevel != 2 || got.AuthPass != "authpw" || got.PrivPass != "privpw" || got.ContextName != "ctx" {
		t.Fatalf("v3 round-trip mismatch: %+v", got)
	}

	// Upsert to v2c.
	d = SNMPDefault{Version: 2, Community: "public", Bulk: 1}
	if err := st.SetSNMPDefault(ctx, "p1", d); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _, _ = st.SNMPDefaultFor(ctx, "p1"); got.Version != 2 || got.Community != "public" {
		t.Fatalf("upsert mismatch: %+v", got)
	}

	// Inherit flags.
	if m, _ := st.SNMPInheritMap(ctx); len(m) != 0 {
		t.Fatalf("expected empty inherit map, got %v", m)
	}
	if err := st.SetSNMPInherit(ctx, "if1", true); err != nil {
		t.Fatalf("SetSNMPInherit: %v", err)
	}
	_ = st.SetSNMPInherit(ctx, "if2", false)
	m, _ := st.SNMPInheritMap(ctx)
	if !m["if1"] || m["if2"] {
		t.Fatalf("inherit map wrong: %v", m)
	}
	if err := st.DeleteSNMPInherit(ctx, "if1"); err != nil {
		t.Fatalf("DeleteSNMPInherit: %v", err)
	}
	if m, _ = st.SNMPInheritMap(ctx); len(m) != 1 {
		t.Fatalf("expected only if2 after delete, got %v", m)
	}
}
