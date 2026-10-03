// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

func TestOSFromSysDescr(t *testing.T) {
	for in, want := range map[string]string{
		"Linux web1 6.1.0-21-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.1.90-1 (2024-05-03) x86_64":                   "Linux 6.1.0-21-amd64 (Debian)",
		"Linux nas 6.12.24-Unraid #1 SMP PREEMPT_DYNAMIC x86_64":                                                 "Linux 6.12.24-Unraid",
		"Hardware: Intel64 Family 6 Model 154 - Software: Windows Version 6.3 (Build 22631 Multiprocessor Free)": "Windows (build 22631)",
		"RouterOS RB4011": "RouterOS RB4011",
	} {
		if got := osFromSysDescr(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestDisplayMAC(t *testing.T) {
	if got := displayMAC("00005e005321"); got != "00:00:5E:00:53:21" {
		t.Fatal(got)
	}
	if got := displayMAC("00-00-5e-00-53-21"); got != "00:00:5E:00:53:21" {
		t.Fatal(got)
	}
	if displayMAC("nope") != "" {
		t.Fatal("not a MAC")
	}
}

// Firmware drift: a device whose controller answers has its word for it (the 2.5G switches run 2.x,
// the others 7.x: never compared across); otherwise older than the newest on exactly the same model is
// flagged; a device with no model, no version or several (a pool on mixed versions) isn't compared.
func TestMarkOlderFirmware(t *testing.T) {
	rows := []inventoryRow{
		{HostID: "1", ClassID: "nas", deviceFacts: deviceFacts{Model: "DXP4800", Firmware: "1.2.0"}},
		{HostID: "2", ClassID: "nas", deviceFacts: deviceFacts{Model: "DXP4800", Firmware: "1.1.0"}},
		{HostID: "3", ClassID: "nas", deviceFacts: deviceFacts{Model: "DXP2800", Firmware: "0.9.0"}},
		{HostID: "4", ClassID: "linux-snmp", deviceFacts: deviceFacts{OS: "Linux 6.1.0-21-amd64 (Debian)"}},
		{HostID: "5", ClassID: "linux-snmp", deviceFacts: deviceFacts{OS: "Linux 6.12.4-1-amd64 (Debian)"}},
		{HostID: "6", ClassID: "xcpng", deviceFacts: deviceFacts{Model: "XCP-ng", Firmware: "8.2.1, 8.3.0"}},
		{HostID: "7", ClassID: "xcpng", deviceFacts: deviceFacts{Model: "XCP-ng", Firmware: "8.3.0"}},
		// UniFi: the controller says; a 2.5G switch on 2.1.8 is current, a Lite on 7.0.50 isn't.
		{HostID: "8", ClassID: "unifi-switch", deviceFacts: deviceFacts{Firmware: "2.1.8.971", upgradeKnown: true}},
		{HostID: "9", ClassID: "unifi-switch", deviceFacts: deviceFacts{Firmware: "7.5.15.17146", upgradeKnown: true}},
		{HostID: "10", ClassID: "unifi-switch", deviceFacts: deviceFacts{Model: "USL8LP", Firmware: "7.0.50", upgradeKnown: true, Upgrade: "7.5.15.17146"}},
	}
	markOlderFirmware(rows)
	want := map[string]string{"2": "1.2.0", "10": "7.5.15.17146"}
	for _, r := range rows {
		if r.Newest != want[r.HostID] {
			t.Errorf("host %s: newest %q, want %q", r.HostID, r.Newest, want[r.HostID])
		}
	}
	if rows[1].NewestFrom != "fleet" || rows[9].NewestFrom != "controller" {
		t.Errorf("where newest comes from: %q %q", rows[1].NewestFrom, rows[9].NewestFrom)
	}
	if less, ok := fwOlder("Debian 12 (bookworm)", "Debian 13 (trixie)"); !ok || !less {
		t.Fatal("Debian 12 is older than 13")
	}
	if _, ok := fwOlder("beta", "1.0"); ok {
		t.Fatal("a version with no number can't be compared")
	}
}

// Links fill in from the host and open only as web pages or remote sessions; a placeholder with no
// value drops the link rather than opening a broken address.
func TestLinks(t *testing.T) {
	h := linkHost{ip: "10.0.0.3", name: "sw floor2", host: "sw-floor2", mac: "00:00:5E:00:53:21", group: "site1/Network",
		macros: map[string]string{"UNIFI.URL": "https://unifi.example.lan/", "UNIFI.SITE": "default"}}
	if u, ok := expandLink("https://{ip}", h); !ok || u != "https://10.0.0.3" {
		t.Fatal(u, ok)
	}
	if u, ok := expandLink("{macro:UNIFI.URL}/network/{macro:UNIFI.SITE}/devices/{mac}", h); !ok || u != "https://unifi.example.lan/network/default/devices/00:00:5E:00:53:21" {
		t.Fatal(u, ok)
	}
	if u, ok := expandLink("https://wiki.example.lan/{group}/{name}", h); !ok || u != "https://wiki.example.lan/site1%2FNetwork/sw%20floor2" {
		t.Fatal(u, ok)
	}
	if _, ok := expandLink("{macro:MISSING}/x", h); ok {
		t.Fatal("a missing macro made a link")
	}
	if _, ok := expandLink("javascript:alert(1)", h); ok {
		t.Fatal("a script address made a link")
	}
	for in, bad := range map[string]bool{
		"https://{ip}": false, "ssh://admin@{ip}": false, "{macro:UNIFI.URL}/x": false,
		"ftp://{ip}": true, "https://{nope}": true, "": true, "javascript:void(0)": true,
	} {
		if got := checkLinkTemplate(in) != ""; got != bad {
			t.Errorf("checkLinkTemplate(%q) refused=%v, want %v", in, got, bad)
		}
	}
	if _, msg := hostOwnLinks([]linkView{{Label: "Floor plan", URL: "https://wiki.example.lan/floor2"}, {}}); msg != "" {
		t.Fatal(msg)
	}
	if _, msg := hostOwnLinks([]linkView{{Label: "", URL: "https://x"}}); msg == "" {
		t.Fatal("a link with no label was taken")
	}
}
