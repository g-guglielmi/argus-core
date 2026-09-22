// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// TestOrderFromList checks a chosen category order ranks the listed categories first (in order) and
// appends the rest in the built-in order, so a partial reorder never drops a category or ties at 0.
func TestOrderFromList(t *testing.T) {
	builtin := categoryOrderServer
	chosen := []string{"Disk", "CPU"} // deliberately partial + out of built-in order
	order := orderFromList(chosen, builtin)

	if order["Disk"] != 0 || order["CPU"] != 1 {
		t.Fatalf("chosen order not honored: Disk=%d CPU=%d", order["Disk"], order["CPU"])
	}
	// Every built-in category must have a rank (nothing silently missing => sorts to 0).
	if len(order) != len(builtin) {
		t.Fatalf("order has %d categories, built-in has %d", len(order), len(builtin))
	}
	// Appended categories keep their built-in relative order and come after the chosen ones.
	if order["Ping"] <= order["CPU"] {
		t.Fatalf("appended Ping (%d) should sort after chosen CPU (%d)", order["Ping"], order["CPU"])
	}
	if order["Ping"] >= order["Memory"] {
		t.Fatalf("appended categories should keep built-in order: Ping=%d Memory=%d", order["Ping"], order["Memory"])
	}
}

// TestCanonicalCategories returns the full category set in built-in server order.
func TestCanonicalCategories(t *testing.T) {
	cats := canonicalCategories()
	if len(cats) != len(categoryOrderServer) {
		t.Fatalf("canonical has %d, built-in has %d", len(cats), len(categoryOrderServer))
	}
	if cats[0] != "Ping" {
		t.Fatalf("expected Ping first, got %q", cats[0])
	}
	for i := 1; i < len(cats); i++ {
		if categoryOrderServer[cats[i-1]] > categoryOrderServer[cats[i]] {
			t.Fatalf("canonical not sorted at %d: %q then %q", i, cats[i-1], cats[i])
		}
	}
	if !validCategories(cats) {
		t.Fatalf("canonical categories must all validate")
	}
	if validCategories([]string{"Ping", "Nonsense"}) {
		t.Fatalf("unknown category must not validate")
	}
}

// TestThresholdSpecFor gates saves to known (template, macro) pairs from the catalog.
func TestThresholdSpecFor(t *testing.T) {
	if _, ok := thresholdSpecFor("Argus Linux by SNMP", "{$CPU.UTIL.WARN}"); !ok {
		t.Fatalf("expected a known threshold to be found")
	}
	if _, ok := thresholdSpecFor("Argus Linux by SNMP", "{$FS.NAME.SKIP}"); ok {
		t.Fatalf("a non-threshold macro must not be editable as a threshold")
	}
	if _, ok := thresholdSpecFor("Argus Nonexistent", "{$CPU.UTIL.WARN}"); ok {
		t.Fatalf("unknown template must not match")
	}
}
