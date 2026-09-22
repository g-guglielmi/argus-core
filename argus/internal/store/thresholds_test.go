// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"reflect"
	"testing"
)

func TestThresholdDefaults(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if m, err := st.ThresholdDefaults(ctx); err != nil || len(m) != 0 {
		t.Fatalf("expected empty, got %+v err=%v", m, err)
	}

	if err := st.SetThresholdDefault(ctx, "Argus Linux by SNMP", "{$CPU.UTIL.WARN}", "70"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Upsert same key overwrites; a second macro coexists.
	if err := st.SetThresholdDefault(ctx, "Argus Linux by SNMP", "{$CPU.UTIL.WARN}", "72"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SetThresholdDefault(ctx, "Argus Base Ping", "{$PING.LOSS.WARN}", "10"); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	m, err := st.ThresholdDefaults(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m["Argus Linux by SNMP"]["{$CPU.UTIL.WARN}"] != "72" || m["Argus Base Ping"]["{$PING.LOSS.WARN}"] != "10" {
		t.Fatalf("unexpected: %+v", m)
	}

	if err := st.DeleteThresholdDefault(ctx, "Argus Linux by SNMP", "{$CPU.UTIL.WARN}"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	m, _ = st.ThresholdDefaults(ctx)
	if _, ok := m["Argus Linux by SNMP"]; ok {
		t.Fatalf("expected the Linux override gone, got %+v", m)
	}
	if m["Argus Base Ping"]["{$PING.LOSS.WARN}"] != "10" {
		t.Fatalf("Base Ping override should remain, got %+v", m)
	}
}

func TestCategoryOrder(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if ord, err := st.CategoryOrder(ctx, "class:linux-snmp"); err != nil || ord != nil {
		t.Fatalf("expected nil, got %v err=%v", ord, err)
	}

	want := []string{"CPU", "Memory", "Disk", "Ping"}
	if err := st.SetCategoryOrder(ctx, "class:linux-snmp", want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := st.CategoryOrder(ctx, "class:linux-snmp")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("get: %v err=%v want %v", got, err, want)
	}

	// Re-save replaces the whole list (not merge).
	next := []string{"Ping", "CPU"}
	if err := st.SetCategoryOrder(ctx, "class:linux-snmp", next); err != nil {
		t.Fatalf("resave: %v", err)
	}
	if got, _ := st.CategoryOrder(ctx, "class:linux-snmp"); !reflect.DeepEqual(got, next) {
		t.Fatalf("expected replace to %v, got %v", next, got)
	}

	// Scopes are independent; an empty list clears the override.
	if err := st.SetCategoryOrder(ctx, "host:1001", []string{"Disk", "Ping"}); err != nil {
		t.Fatalf("host set: %v", err)
	}
	if err := st.SetCategoryOrder(ctx, "class:linux-snmp", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := st.CategoryOrder(ctx, "class:linux-snmp"); got != nil {
		t.Fatalf("expected cleared, got %v", got)
	}
	if got, _ := st.CategoryOrder(ctx, "host:1001"); !reflect.DeepEqual(got, []string{"Disk", "Ping"}) {
		t.Fatalf("host order should be untouched, got %v", got)
	}
}
