// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// A host's own facts, links and journal; the link templates start with one "Web UI" once.
func TestDeviceStore(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	tpls, err := st.LinkTemplates(ctx)
	if err != nil || len(tpls) != 1 || tpls[0].Label != "Web UI" || tpls[0].URL != "https://{ip}" || len(tpls[0].Classes) == 0 {
		t.Fatalf("seeded templates: %+v %v", tpls, err)
	}
	if err := st.DeleteLinkTemplate(ctx, tpls[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.seedLinkTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if tpls, _ := st.LinkTemplates(ctx); len(tpls) != 0 {
		t.Fatal("a deleted Web UI came back")
	}
	id, err := st.SaveLinkTemplate(ctx, LinkTemplate{Label: "SSH", URL: "ssh://admin@{ip}", Classes: []string{"linux-ssh"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveLinkTemplate(ctx, LinkTemplate{ID: id, Label: "SSH", URL: "ssh://root@{ip}"}); err != nil {
		t.Fatal(err)
	}
	if tpls, _ := st.LinkTemplates(ctx); len(tpls) != 1 || tpls[0].URL != "ssh://root@{ip}" || len(tpls[0].Classes) != 0 {
		t.Fatalf("after update: %+v", tpls)
	}

	if err := st.SetHostFacts(ctx, "17", HostOwnFacts{AssetTag: "IT-0412", Location: "Floor 2"}); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.HostFacts(ctx); f["17"].AssetTag != "IT-0412" || f["17"].Location != "Floor 2" {
		t.Fatalf("facts: %+v", f)
	}
	_ = st.SetHostFacts(ctx, "17", HostOwnFacts{})
	if f, _ := st.HostFacts(ctx); len(f) != 0 {
		t.Fatal("empty facts kept a row")
	}

	if err := st.SetHostLinks(ctx, "17", []Link{{Label: "Floor plan", URL: "https://wiki.example.lan/f2"}, {Label: "Rack", URL: "https://wiki.example.lan/r1"}}); err != nil {
		t.Fatal(err)
	}
	if ls, _ := st.HostLinks(ctx, "17"); len(ls) != 2 || ls[1].Label != "Rack" {
		t.Fatalf("links: %+v", ls)
	}

	a, _ := st.AddJournalEntry(ctx, JournalEntry{HostID: "17", Kind: "warning", Text: " Fan noisy ", ByUser: 2, ByName: "Bob", CreatedAt: 100})
	b, _ := st.AddJournalEntry(ctx, JournalEntry{HostID: "17", Kind: "info", Text: "PSU replaced", ByUser: 1, ByName: "Alice", CreatedAt: 200})
	j, _ := st.HostJournal(ctx, "17")
	if len(j) != 2 || j[0].ID != b.ID || j[1].Text != "Fan noisy" {
		t.Fatalf("journal: %+v", j)
	}
	if n, _ := st.JournalCounts(ctx); n["17"] != 2 {
		t.Fatalf("counts: %v", n)
	}
	if err := st.DeleteJournalEntry(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.JournalEntryByID(ctx, a.ID); err != ErrNotFound {
		t.Fatalf("deleted entry: %v", err)
	}
}

// An automatic upstream answer keeps who gave it; a row from before sources read as the controller's,
// and setting the mode by hand leaves the answer alone.
func TestHostUpstreamSource(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetAutoUpstream(ctx, "60", "50", "", "xcpng", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO host_upstream (host_id, auto_host, auto_port, auto_at) VALUES ('61', '9', '3', 100)`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpstreamMode(ctx, "60", "manual", "7"); err != nil {
		t.Fatal(err)
	}
	all, err := st.HostUpstreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u := all["60"]; u.AutoHost != "50" || u.AutoSource != "xcpng" || u.Mode != "manual" || u.ManualHost != "7" {
		t.Fatalf("60: %+v", u)
	}
	if u := all["61"]; u.AutoSource != "controller" || u.AutoPort != "3" {
		t.Fatalf("61: %+v", u)
	}
	_ = st.SetAutoUpstream(ctx, "60", "51", "", "controller", 200)
	all, _ = st.HostUpstreams(ctx)
	if u := all["60"]; u.AutoHost != "51" || u.AutoSource != "controller" || u.AutoAt != 200 {
		t.Fatalf("60 after: %+v", u)
	}
}
