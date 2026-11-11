// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"path/filepath"
	"testing"

	"argus/internal/store"
)

// Three Citrix services renamed in one upgrade, among services that kept their names: each lost one
// is paired with its new name, the strongest first; the pair sharing one word says to check it; the
// sensors that read before the loss are never taken for new ones.
func TestRenamePairsCitrix(t *testing.T) {
	const lostAt = 1_000_000
	svc := func(id, name string, lost bool) renameCand {
		c := renameCand{id: id, name: name, label: name, vt: "3", parent: "proto"}
		if lost {
			c.lost, c.last, c.removed = true, lostAt, lostAt+7*86400
		}
		return c
	}
	cands := []renameCand{
		svc("1", "Citrix AD Identity Service", false), svc("2", "Citrix Broker Service", false),
		svc("3", "Citrix Configuration Service", false), svc("4", "Citrix Host Service", false),
		svc("5", "Citrix Monitor Service", false),
		svc("10", "Citrix StoreFront Service", false), svc("11", "Citrix Broker Agent Service", false),
		svc("12", "Citrix Telemetry Collector", false),
		svc("20", "Citrix Storefront Service", true), svc("21", "Citrix Broker Agent", true),
		svc("22", "Citrix Telemetry Service", true),
		{id: "30", name: "Citrix Telemetry Service", vt: "3", parent: "other", lost: true, last: lostAt}, // another rule
	}
	newOnes := map[string]bool{"10": true, "11": true, "12": true}
	hasData := func(ids []string, from, to int64) map[string]map[int64]bool {
		if to != lostAt-3600 {
			t.Fatalf("asked for readings until %d", to)
		}
		out := map[string]map[int64]bool{}
		for _, id := range ids {
			if !newOnes[id] { // the others read every hour, side by side
				out[id] = hoursOf(0, 300)
			}
		}
		return out
	}
	pairs := renamePairs(cands, func(string) bool { return false }, func(string) bool { return false }, hasData)
	want := map[string]struct {
		new, why string
		check    bool
	}{
		"20": {"10", "the same words", false},
		"21": {"11", "the new name adds a word", false},
		"22": {"12", `only "Telemetry" in common: check it`, true},
	}
	if len(pairs) != len(want) {
		t.Fatalf("got %d pairs: %+v", len(pairs), pairs)
	}
	for _, p := range pairs {
		w, ok := want[p.Old.ID]
		if !ok || p.New != w.new || p.Why != w.why || p.Check != w.check {
			t.Fatalf("pair %+v, want %+v", p, w)
		}
		if len(p.Options) != 3 {
			t.Fatalf("options %+v, want the 3 new sensors", p.Options)
		}
		if p.Removed != lostAt+7*86400 {
			t.Fatalf("removed %d", p.Removed)
		}
	}
	// One decided already, one new sensor linked already: neither comes back.
	pairs = renamePairs(cands, func(id string) bool { return id == "20" }, func(id string) bool { return id == "11" }, hasData)
	if len(pairs) != 1 || pairs[0].Old.ID != "22" {
		t.Fatalf("after decisions: %+v", pairs)
	}
}

// Names that share only words every name of the rule has aren't suggested.
func TestRenamePairsTooWeak(t *testing.T) {
	cands := []renameCand{
		{id: "1", name: "Citrix Service A", vt: "3", parent: "p"},
		{id: "2", name: "Citrix Print Service", vt: "3", parent: "p", lost: true, last: 5000},
		{id: "3", name: "Citrix Licensing Service", vt: "3", parent: "p"},
	}
	none := func(ids []string, from, to int64) map[string]map[int64]bool {
		return map[string]map[int64]bool{"1": hoursOf(0, 10)}
	}
	if p := renamePairs(cands, func(string) bool { return false }, func(string) bool { return false }, none); len(p) != 0 {
		t.Fatalf("suggested %+v", p)
	}
	// A different kind of value is never a pair.
	cands[2].name, cands[2].vt = "Citrix Print Service", "0"
	if p := renamePairs(cands, func(string) bool { return false }, func(string) bool { return false }, none); len(p) != 0 {
		t.Fatalf("paired across value types: %+v", p)
	}
}

// A sensor renamed twice reads all its earlier names, nearest first; one kept apart links nothing.
func TestRenameIndexChain(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, r := range []store.SensorRename{
		{OldID: "a", NewID: "b", HostID: "h", OldName: "A", Until: 10, At: 1},
		{OldID: "b", NewID: "c", HostID: "h", OldName: "B", Until: 20, At: 2},
		{OldID: "x", HostID: "h", OldName: "X", At: 3},
	} {
		if err := st.PutSensorRename(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	x := &renameIndex{}
	if err := x.load(ctx, st); err != nil {
		t.Fatal(err)
	}
	if c := x.chain("c"); len(c) != 2 || c[0] != "b" || c[1] != "a" {
		t.Fatalf("chain %v", c)
	}
	if !x.linkedOld("a") || x.linkedOld("x") || !x.isDecided("x") || x.isDecided("c") {
		t.Fatal("decisions read wrong")
	}
	if w, ok := x.wasOf("c"); !ok || w.OldName != "B" {
		t.Fatalf("was %+v", w)
	}
	s := &Server{ren: x}
	if m := s.renameMarks(map[string]bool{"c": true}, 0, 100); len(m) != 2 {
		t.Fatalf("marks %+v", m)
	}
	var nilIdx *renameIndex
	if nilIdx.linkedOld("a") || nilIdx.chain("c") != nil {
		t.Fatal("a nil index must read empty")
	}
}

// hoursOf is the hours [from, to) as unix hour clocks.
func hoursOf(from, to int) map[int64]bool {
	out := map[int64]bool{}
	for h := from; h < to; h++ {
		out[int64(h)*3600] = true
	}
	return out
}

// A Windows service table that lists a service under its internal name at times and its display
// name at others: the display name's sensor read before the loss, but took turns with the old one
// (or began lately), so it is still its new name; a service that read alongside the old one all
// along (Netbird) is another sensor.
func TestRenamePairsTookTurns(t *testing.T) {
	const lostAt = 2_000_000
	cands := []renameCand{
		{id: "1", name: "Cloudflared", vt: "3", parent: "p", lost: true, last: lostAt},
		{id: "2", name: "RustDesk", vt: "3", parent: "p", lost: true, last: lostAt},
		{id: "3", name: "Cloudflared agent", vt: "3", parent: "p"},
		{id: "4", name: "RustDesk Service", vt: "3", parent: "p"},
		{id: "5", name: "Netbird", vt: "3", parent: "p"},
	}
	hours := func(ids []string, from, to int64) map[string]map[int64]bool {
		turns := hoursOf(0, 100)
		for h := range hoursOf(200, 300) {
			turns[h] = true
		}
		return map[string]map[int64]bool{
			"1": hoursOf(100, 200), "2": hoursOf(100, 200), // the old names, between the display name's turns
			"3": turns, "4": hoursOf(190, 300), // RustDesk Service began lately, 10 hours alongside
			"5": hoursOf(0, 300),
		}
	}
	pairs := renamePairs(cands, func(string) bool { return false }, func(string) bool { return false }, hours)
	if len(pairs) != 2 || pairs[0].Old.ID != "1" || pairs[0].New != "3" || pairs[1].Old.ID != "2" || pairs[1].New != "4" {
		t.Fatalf("pairs %+v", pairs)
	}
	for _, p := range pairs {
		for _, o := range p.Options {
			if o.ID == "5" {
				t.Fatalf("Netbird, read alongside all along, offered: %+v", p)
			}
		}
	}
	// The readings can't be read: nothing is taken for new.
	failed := func([]string, int64, int64) map[string]map[int64]bool { return nil }
	if p := renamePairs(cands, func(string) bool { return false }, func(string) bool { return false }, failed); len(p) != 0 {
		t.Fatalf("paired without readings: %+v", p)
	}
}
