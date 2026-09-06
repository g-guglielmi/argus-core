package store

import (
	"context"
	"testing"
)

func TestDeviceClassOverlay(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Unset host: no class.
	if _, ok, err := st.GetDeviceClass(ctx, "1001"); err != nil || ok {
		t.Fatalf("expected no class, got ok=%v err=%v", ok, err)
	}

	// Set then read back.
	if err := st.SetDeviceClass(ctx, "1001", "linux-snmp", "manual"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if id, ok, err := st.GetDeviceClass(ctx, "1001"); err != nil || !ok || id != "linux-snmp" {
		t.Fatalf("get: id=%q ok=%v err=%v", id, ok, err)
	}

	// Empty source defaults to manual (and upsert overwrites class).
	if err := st.SetDeviceClass(ctx, "1001", "unifi-switch", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if id, _, _ := st.GetDeviceClass(ctx, "1001"); id != "unifi-switch" {
		t.Fatalf("expected overwrite to unifi-switch, got %q", id)
	}

	// Second host + bulk map.
	if err := st.SetDeviceClass(ctx, "1002", "base", "discovered"); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	m, err := st.DeviceClasses(ctx)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(m) != 2 || m["1001"] != "unifi-switch" || m["1002"] != "base" {
		t.Fatalf("unexpected map: %+v", m)
	}

	// Delete.
	if err := st.DeleteDeviceClass(ctx, "1001"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := st.GetDeviceClass(ctx, "1001"); ok {
		t.Fatalf("expected 1001 gone")
	}
}
