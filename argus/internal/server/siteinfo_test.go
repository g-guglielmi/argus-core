// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"

	"argus/internal/store"
)

// An alert says who to call about the internet line its sensor measures (any sensor of the same UniFi
// WAN, or any sensor of a host a line is tied to as a whole), every line when the site's probe
// stopped reporting, and the site's first contact.
func TestWhoToCall(t *testing.T) {
	info := store.SiteInfo{
		Site:     "site1",
		Contacts: []store.SiteContact{{Role: "On-site IT", Email: "it@example.com"}, {Role: "On-site IT", Name: "Bob Example", Phone: "+1 555 0101"}},
		Lines: []store.SiteLine{
			{Name: "WAN 1", HostID: "10", Key: "unifi.wan.avail[1]", Provider: "Example Fiber", Circuit: "EXF-000123", Phone: "+1 555 0100"},
			{Name: "WAN 2", HostID: "10", Key: "unifi.wan.avail[2]", Provider: "Example Mobile", Circuit: "SIM 8900000000000000001"},
			{Name: "Backup DSL", HostID: "20", Provider: "Example DSL"},
		},
	}
	if siteOf([]string{"site1/Network", "site2"}) != "site1" || siteOf(nil) != "" {
		t.Fatal("siteOf")
	}
	if got := callLines(info, "10", "unifi.wan.latency[1]"); strings.Join(got, "|") != "Call Example Fiber +1 555 0100, circuit EXF-000123 (WAN 1)|On-site IT: Bob Example +1 555 0101" {
		t.Fatalf("WAN 1 latency: %q", got)
	}
	if got := linesFor(info, "10", "unifi.wan.in[2]"); len(got) != 1 || got[0].Name != "WAN 2" {
		t.Fatalf("WAN 2 traffic: %+v", got)
	}
	if got := linesFor(info, "10", "icmpping"); len(got) != 0 {
		t.Fatalf("the gateway's ping named a line: %+v", got)
	}
	if got := linesFor(info, "20", "icmpping"); len(got) != 1 || got[0].Provider != "Example DSL" {
		t.Fatalf("a line tied to a whole host: %+v", got)
	}
	if got := linesFor(info, "30", probeMasterKey); len(got) != 3 {
		t.Fatalf("the probe stopped reporting: %+v", got)
	}
	if got := rowCall(linesFor(info, "10", "unifi.wan.avail[1]")); got != "Example Fiber · circuit EXF-000123 · support +1 555 0100" {
		t.Fatalf("row call: %q", got)
	}
	if got := callLines(store.SiteInfo{}, "10", "unifi.wan.avail[1]"); got != nil {
		t.Fatalf("no info, no call: %q", got)
	}
}
