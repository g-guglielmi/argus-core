// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"path/filepath"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// A note's sensor is open while any warning-or-worse problem is on it (an interface problem by its own
// id); a problem whose trigger couldn't be read makes the answer unknown, so nothing is cleared.
func TestOpenSensorKeys(t *testing.T) {
	problems := []zabbix.Problem{
		{EventID: "e1", ObjectID: "t1", Severity: "4"},
		{EventID: "e2", ObjectID: "t2", Severity: "1"}, // information: not a problem for a note
		{EventID: "argus-interface-9", ObjectID: "argus-interface-9", Severity: "4"},
	}
	targets := map[string]zabbix.TriggerTarget{
		"t1":                {Items: []zabbix.TargetItem{{ItemID: "100"}, {ItemID: "101"}}},
		"t2":                {Items: []zabbix.TargetItem{{ItemID: "200"}}},
		"argus-interface-9": {Hosts: []zabbix.TargetHost{{HostID: "10"}}},
	}
	open, ok := openSensorKeys(problems, targets)
	if !ok || !open["100"] || !open["101"] || open["200"] || !open["argus-interface-9"] {
		t.Fatalf("open = %v (%v)", open, ok)
	}
	delete(targets, "t1")
	if _, ok := openSensorKeys(problems, targets); ok {
		t.Fatal("an unreadable trigger still gave an answer")
	}
}

// A note survives a blip: its sensor seen OK starts a countdown, a problem again stops it, and only a
// sensor OK for the whole grace loses its note, as of when it went OK.
func TestPlanNoteSweep(t *testing.T) {
	notes := map[string]store.SensorNote{
		"a": {ID: 1, Key: "a"},                // still in trouble
		"b": {ID: 2, Key: "b"},                // just went OK
		"c": {ID: 3, Key: "c", OKSince: 1000}, // OK, within the grace
		"d": {ID: 4, Key: "d", OKSince: 900},  // OK for the grace: cleared
		"e": {ID: 5, Key: "e", OKSince: 1000}, // in trouble again
	}
	open := map[string]bool{"a": true, "e": true}
	p := planNoteSweep(notes, open, 90, 1050)
	if len(p.okSince) != 1 || p.okSince[0] != 2 {
		t.Errorf("okSince = %v", p.okSince)
	}
	if len(p.reopen) != 1 || p.reopen[0] != 5 {
		t.Errorf("reopen = %v", p.reopen)
	}
	if len(p.clear) != 1 || p.clear["d"] != 900 {
		t.Errorf("clear = %v", p.clear)
	}
}

// The history gives an incident the note its sensor had while it was open, and none from another time.
func TestAttachIncidentNotes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.SetSensorNote(ctx, "100", "10", "ISP ticket open", 1, "Alice Rossi", 1100); err != nil {
		t.Fatal(err)
	}
	_ = st.ClearSensorNote(ctx, "100", 1300)
	if _, err := st.SetSensorNote(ctx, "argus-interface-9", "10", "Switch replaced", 1, "Bob", 1500); err != nil {
		t.Fatal(err)
	}
	out := []incidentView{
		{EventID: "e1", ItemID: "100", Start: 1000, End: 1290}, // the note was on it
		{EventID: "e2", ItemID: "100", Start: 2000, End: 2100}, // a later incident: no note then
		{EventID: "argus-interface-9", Start: 1400},            // still open, noted by its own id
	}
	attachIncidentNotes(ctx, st, out, 0)
	if out[0].Note != "ISP ticket open" || out[0].NoteBy != "Alice Rossi" {
		t.Errorf("incident 1: %+v", out[0])
	}
	if out[1].Note != "" {
		t.Errorf("incident 2 got a note from another time: %+v", out[1])
	}
	if out[2].Note != "Switch replaced" {
		t.Errorf("interface incident: %+v", out[2])
	}
}
