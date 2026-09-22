// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"

	"argus/internal/store"
	"argus/internal/unifi"
)

func TestNormalizeScanCIDR(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"10.0.0.0/24", "10.0.0.0/24", false},
		{" 10.0.0.0/24 ", "10.0.0.0/24", false},
		{"10.0.0.55/24", "10.0.0.0/24", false}, // canonicalised to the network address
		{"10.0.0.5", "10.0.0.5/32", false},     // bare IP = /32
		{"10.0.0.0/22", "10.0.0.0/22", false},  // largest allowed
		{"10.0.0.0/21", "", true},              // too big
		{"10.0.0.0/8", "", true},
		{"", "", true},
		{"not-a-subnet", "", true},
		{"fd00::/64", "", true}, // IPv4 only
		{"10.0.0.0/33", "", true},
	}
	for _, c := range cases {
		got, msg := normalizeScanCIDR(c.in)
		if c.wantErr && msg == "" {
			t.Errorf("%q: expected an error, got %q", c.in, got)
		}
		if !c.wantErr && (msg != "" || got != c.want) {
			t.Errorf("%q: got (%q, %q), want %q", c.in, got, msg, c.want)
		}
	}
}

// Scan rows matching a saved controller's devices get the controller facts + reference merged in
// (MAC first, IP as the fallback for probes that see no ARP); rows the controller doesn't know,
// and rows that already carry facts, are untouched.
func TestEnrichScanResults(t *testing.T) {
	inv := []controllerInventory{{ID: 7, Devices: []unifi.Device{
		{IP: "10.0.0.2", MAC: "aa:bb:cc:00:00:01", Name: "sw-rack", Model: "US8P60", Type: "usw", State: 1, Version: "7.1.26", Site: "default"},
		{IP: "10.0.0.9", MAC: "aa:bb:cc:00:00:02", Name: "ap-hall", Model: "U7PG2", Type: "uap", State: 1, Site: "default"},
	}, Clients: []unifi.Client{
		{IP: "10.0.0.77", MAC: "aa:bb:cc:00:00:77", Name: "nas-lab", Hostname: "nas-lab.example.lan", Wired: true},
	}}}
	results := []store.DiscoveryResult{
		{IP: "10.0.0.2", MAC: "AA-BB-CC-00-00-01"},                  // MAC match, other notation
		{IP: "10.0.0.9"},                                            // no MAC (routed probe) -> IP match
		{IP: "10.0.0.50", MAC: "11:22:33:44:55:66"},                 // unknown device
		{IP: "10.0.0.60", UniFiJSON: `{"type":"usw"}`, ControllerID: 3}, // already has facts
		{IP: "10.0.0.77"},                                           // controller CLIENT -> naming hint only
	}
	if n := enrichScanResults(results, inv); n != 3 {
		t.Fatalf("enriched %d rows, want 3", n)
	}
	if results[0].ControllerID != 7 || !strings.Contains(results[0].UniFiJSON, `"sw-rack"`) || results[0].SuggestedClass != "unifi-switch" {
		t.Fatalf("MAC-matched row wrong: %+v", results[0])
	}
	if results[1].ControllerID != 7 || !strings.Contains(results[1].UniFiJSON, `"ap-hall"`) || results[1].SuggestedClass != "unifi-ap" {
		t.Fatalf("IP-matched row wrong: %+v", results[1])
	}
	if results[2].UniFiJSON != "" || results[2].ControllerID != 0 {
		t.Fatalf("unknown row must stay untouched: %+v", results[2])
	}
	if results[3].ControllerID != 3 || results[3].UniFiJSON != `{"type":"usw"}` {
		t.Fatalf("pre-facted row must stay untouched: %+v", results[3])
	}
	if !strings.Contains(results[4].UniFiClient, `"nas-lab"`) || !strings.Contains(results[4].UniFiClient, `"wired":true`) {
		t.Fatalf("client-matched row must carry the naming hint: %+v", results[4])
	}
	if results[4].UniFiJSON != "" || results[4].ControllerID != 0 || results[4].SuggestedClass != "" {
		t.Fatalf("a client hint must never bring device facts or a class: %+v", results[4])
	}
	if n := enrichScanResults(results, nil); n != 0 {
		t.Fatalf("no inventories must be a no-op, enriched %d", n)
	}
}
