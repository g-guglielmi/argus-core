// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"testing"
)

// Tags live on hosts and probes; a rename carries through to them and to the channels limited to the
// tag, a delete drops it everywhere, and an unknown tag can't be put on a host.
func TestTags(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for _, tg := range []Tag{{Name: "critical", Color: "#e5484d"}, {Name: "poe", Color: "#f5a524"}, {Name: "customer-a", Color: "#8e4ec6"}} {
		if err := st.CreateTag(ctx, tg); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateTag(ctx, Tag{Name: "Critical"}); !errors.Is(err, ErrTagExists) {
		t.Fatalf("a second tag differing only in case: %v", err)
	}
	if err := st.SetHostTags(ctx, "17", []string{"poe", "critical", "poe"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetHostTags(ctx, "18", []string{"no-such-tag"}); err == nil {
		t.Fatal("an unknown tag went on a host")
	}
	if err := st.SetProbeTags(ctx, "10", []string{"customer-a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ChangeHostTags(ctx, []string{"17", "21"}, []string{"customer-a"}, []string{"poe"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateNotifyChannel(ctx, NotifyChannel{Type: "teams", Name: "Teams IT", Enabled: true, Tags: []string{"critical", "poe"}, Config: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}

	host, _ := st.HostTags(ctx)
	if got := host["17"]; len(got) != 2 || got[0] != "critical" || got[1] != "customer-a" {
		t.Fatalf("host 17: %v", got)
	}
	if got := host["21"]; len(got) != 1 || got[0] != "customer-a" {
		t.Fatalf("host 21: %v", got)
	}

	if err := st.UpdateTag(ctx, "critical", Tag{Name: "tier-1", Color: "#e5484d", Description: "stops work"}); err != nil {
		t.Fatal(err)
	}
	host, _ = st.HostTags(ctx)
	if got := host["17"]; len(got) != 2 || got[0] != "customer-a" || got[1] != "tier-1" {
		t.Fatalf("after rename: %v", got)
	}
	if c, _ := st.GetNotifyChannel(ctx, id); len(c.Tags) != 2 || c.Tags[0] != "tier-1" || c.Tags[1] != "poe" {
		t.Fatalf("channel after rename: %v", c.Tags)
	}

	if err := st.DeleteTag(ctx, "customer-a"); err != nil {
		t.Fatal(err)
	}
	host, _ = st.HostTags(ctx)
	probe, _ := st.ProbeTags(ctx)
	if len(host["21"]) != 0 || len(probe["10"]) != 0 || len(host["17"]) != 1 {
		t.Fatalf("after delete: hosts %v probes %v", host, probe)
	}
	if err := st.DeleteTag(ctx, "poe"); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetNotifyChannel(ctx, id); len(c.Tags) != 1 || c.Tags[0] != "tier-1" {
		t.Fatalf("channel after delete: %v", c.Tags)
	}
}
