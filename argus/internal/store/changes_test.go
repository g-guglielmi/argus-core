// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// The change log keeps one entry per action with the hosts it touched: a host's own list finds the
// bulk action that named it, the filters narrow by text and kind, and old entries prune.
func TestChangeLog(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	err := st.AddChanges(ctx, []Change{
		{At: 100, ActorKind: "user", ActorID: 1, Actor: "Alice Rossi", Category: "states", Action: "Paused 3 hosts", Object: "ap-lobby, ap-office, cam-entrance",
			Detail: "for 2 h", Reason: "replacing a power supply", HostIDs: []string{"21", "22", "23", "22"}},
		{At: 200, ActorKind: "user", ActorID: 1, Actor: "Alice Rossi", Category: "thresholds", Action: "Changed a default threshold", Object: "Argus UniFi Switch",
			Diff: []ChangeDiff{{Field: "Temperature warning", Old: "70 °C", New: "65 °C"}}},
		{At: 300, ActorKind: "argus", Actor: "Argus", Category: "hosts", Action: "Upstream device", Object: "ap-lobby", HostIDs: []string{"21"},
			Diff: []ChangeDiff{{Field: "Upstream", Old: "", New: "sw-core port 24"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	all, err := st.ListChanges(ctx, ChangeQuery{})
	if err != nil || len(all) != 3 || all[0].At != 300 || all[2].Action != "Paused 3 hosts" {
		t.Fatalf("all: %+v %v", all, err)
	}
	if got := all[2].HostIDs; len(got) != 3 || got[0] != "21" || got[2] != "23" {
		t.Fatalf("bulk entry hosts (deduped, in order): %v", got)
	}
	if d := all[1].Diff; len(d) != 1 || d[0].Old != "70 °C" || d[0].New != "65 °C" {
		t.Fatalf("diff: %+v", d)
	}

	host, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{"21"}})
	if len(host) != 2 || host[0].ActorKind != "argus" || host[1].Reason != "replacing a power supply" {
		t.Fatalf("host 21's changes: %+v", host)
	}
	if none, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{}}); len(none) != 0 {
		t.Fatalf("an empty host filter matched %d", len(none))
	}
	if txt, _ := st.ListChanges(ctx, ChangeQuery{Text: "TEMPERATURE"}); len(txt) != 1 || txt[0].Category != "thresholds" {
		t.Fatalf("text search reads the diff: %+v", txt)
	}
	if cat, _ := st.ListChanges(ctx, ChangeQuery{Category: "states", From: 50}); len(cat) != 1 {
		t.Fatalf("category: %+v", cat)
	}
	if page, _ := st.ListChanges(ctx, ChangeQuery{Before: all[0].ID, Limit: 1}); len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("paging back: %+v", page)
	}

	if err := st.PruneChanges(ctx, 250); err != nil {
		t.Fatal(err)
	}
	left, _ := st.ListChanges(ctx, ChangeQuery{})
	if len(left) != 1 || left[0].At != 300 {
		t.Fatalf("after prune: %+v", left)
	}
	if host, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{"22"}}); len(host) != 0 {
		t.Fatalf("pruned entry still listed for its host: %+v", host)
	}
}
