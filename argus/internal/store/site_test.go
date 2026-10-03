// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// A site's info is kept whole, moves along with a renamed site, and an empty one is removed.
func TestSiteInfoStore(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if i, err := st.SiteInfoFor(ctx, "site1"); err != nil || !i.Empty() || i.Contacts == nil || i.Lines == nil {
		t.Fatalf("nothing kept yet: %+v %v", i, err)
	}
	in := SiteInfo{
		Site: "site1", Address: "1 Example Street",
		Contacts: []SiteContact{{Role: "On-site IT", Name: "Bob Example", Phone: "+1 555 0101"}},
		Lines:    []SiteLine{{Name: "WAN 1", HostID: "10", Key: "unifi.wan.avail[1]", Provider: "Example Fiber", Circuit: "EXF-000123", Phone: "+1 555 0100"}},
	}
	if err := st.SetSiteInfo(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SiteInfoFor(ctx, "site1")
	if got.Address != in.Address || len(got.Contacts) != 1 || got.Lines[0].Circuit != "EXF-000123" || got.UpdatedAt == 0 {
		t.Fatalf("read back: %+v", got)
	}
	if err := st.RenameSiteInfo(ctx, "site1", "site9"); err != nil {
		t.Fatal(err)
	}
	all, _ := st.SiteInfos(ctx)
	if _, old := all["site1"]; old || all["site9"].Address != in.Address {
		t.Fatalf("after rename: %+v", all)
	}
	// A rename onto a site with its own info leaves both alone.
	_ = st.SetSiteInfo(ctx, SiteInfo{Site: "site2", Note: "own"})
	_ = st.RenameSiteInfo(ctx, "site9", "site2")
	if all, _ := st.SiteInfos(ctx); all["site2"].Note != "own" || all["site9"].Address == "" {
		t.Fatalf("rename onto a site with info: %+v", all)
	}
	if err := st.SetSiteInfo(ctx, SiteInfo{Site: "site9"}); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.SiteInfos(ctx); len(all) != 1 {
		t.Fatalf("an emptied site was kept: %+v", all)
	}
}
