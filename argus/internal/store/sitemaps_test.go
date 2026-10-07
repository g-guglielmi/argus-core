// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// A map is off until turned on; turning it off keeps its layout; a removed probe takes its map along.
func TestSiteMapStore(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if m, err := st.SiteMapFor(ctx, "proxy-site1"); err != nil || m.On || m.Pins == nil || len(m.Pins) != 0 {
		t.Fatalf("off by default: %+v %v", m, err)
	}
	if err := st.SetSiteMapOn(ctx, "proxy-site1", true, "Ada Admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSiteMapPins(ctx, "proxy-site1", map[string]MapPin{"10": {DX: 40, DY: -12}}); err != nil {
		t.Fatal(err)
	}
	m, _ := st.SiteMapFor(ctx, "proxy-site1")
	if !m.On || m.OnBy != "Ada Admin" || m.OnAt == 0 || m.Pins["10"].DX != 40 {
		t.Fatalf("on, with a pin: %+v", m)
	}
	if err := st.SetSiteMapOn(ctx, "proxy-site1", false, ""); err != nil {
		t.Fatal(err)
	}
	m, _ = st.SiteMapFor(ctx, "proxy-site1")
	if m.On || m.OnBy != "" || m.Pins["10"].DY != -12 {
		t.Fatalf("off keeps the layout: %+v", m)
	}
	// Moving a device on a map never turns it on.
	_ = st.SetSiteMapPins(ctx, "proxy-site2", map[string]MapPin{"20": {DX: 1}})
	all, _ := st.SiteMaps(ctx)
	if all["proxy-site2"].On || all["proxy-site2"].Pins["20"].DX != 1 {
		t.Fatalf("pins alone: %+v", all)
	}
	// Turning off a map that was never on is nothing.
	if err := st.SetSiteMapOn(ctx, "proxy-site3", false, ""); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteProxyRecords(ctx, "101", "proxy-site1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReconcileProxies(ctx, map[string]bool{"proxy-site1": true}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	all, _ = st.SiteMaps(ctx)
	if len(all) != 0 {
		t.Fatalf("removed and pruned probes keep no map: %+v", all)
	}
}
