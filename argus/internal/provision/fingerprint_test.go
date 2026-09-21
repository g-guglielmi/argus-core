// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import "testing"

func TestSuggestClass(t *testing.T) {
	cases := []struct {
		name string
		f    Fingerprint
		want string
	}{
		{"adguard by title", Fingerprint{HTTPTitle: "AdGuard Home", TCP: []int{53, 80}, DNS: true}, "adguard"},
		{"home assistant by title", Fingerprint{HTTPTitle: "Home Assistant", TCP: []int{8123}}, "home-assistant"},
		{"xcp-ng by title", Fingerprint{HTTPTitle: "Welcome to XCP-ng", TCP: []int{443, 22}}, "xcpng"},
		// An XCP-NG host's own :443 page titles itself "XO Lite" (lab-confirmed).
		{"xcp-ng via XO Lite", Fingerprint{HTTPTitle: "XO Lite", TCP: []int{22, 80, 443}}, "xcpng"},
		{"unifi ap", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.6", SysDescr: "UAP-AC-Pro 6.6.55"}, "unifi-ap"},
		{"unifi ap bare oid", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112", SysDescr: "U6-Pro 6.8.2", SysName: "office-ap"}, "unifi-ap"},
		{"unifi switch", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.5", SysDescr: "USW-24-PoE"}, "unifi-switch"},
		// Old UniFi firmware answers with the stock net-snmp OID; only sysDescr/sysName give it
		// away (lab-confirmed on a USW SFP) - and the "unraid"-ish sysname must not win.
		{"unifi switch, old firmware", Fingerprint{SysObjectID: ".1.3.6.1.4.1.8072.3.2.10", SysDescr: "Linux UBNT 3.18.24 mips", SysName: "rack-USW-SFP-unraid-link"}, "unifi-switch"},
		{"unifi gateway", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.4", SysName: "UDM-Pro"}, "unifi-gateway"},
		// A US-8 on old firmware: net-snmp OID, no "UBNT" anywhere - only the model tokens give it
		// away (lab-confirmed). Strong model tokens must open the UniFi branch on their own.
		{"unifi switch by model token only", Fingerprint{SysObjectID: ".1.3.6.1.4.1.8072.3.2.10", SysDescr: "US-8-60W, 7.5.15.17146, Linux 3.6.5", SysName: "garage-USW8PoE"}, "unifi-switch"},
		// A gateway titles its login page "UniFi OS" (lab-confirmed - UDM/UCG/UXG run UniFi OS)
		// while its sysDescr is plain Linux; a self-hosted Network Server (the Debian-VM console)
		// titles it "UniFi Network". The title must win over the OS checks.
		{"unifi gateway by title", Fingerprint{HTTPTitle: "UniFi OS", SysDescr: "Linux gw 4.19", TCP: []int{22, 53, 80, 443, 8443}, DNS: true}, "unifi-gateway"},
		{"unifi console by title", Fingerprint{HTTPTitle: "UniFi Network", SysDescr: "Linux console-vm 6.1 amd64", TCP: []int{22, 8443}}, "unifi-console"},
		// The MAC OUI identifies Ubiquiti gear that answers nothing but SSH (the USW-Flex/Ultra
		// models ship no SNMP agent) - lab-confirmed on a site full of them. A UniFi device that
		// answers DNS is the gateway.
		{"unifi switch by mac only", Fingerprint{MAC: "f4:e2:c6:12:34:56", TCP: []int{22}}, "unifi-switch"},
		{"unifi gateway by mac + dns", Fingerprint{MAC: "F4-E2-C6-AA-BB-CC", TCP: []int{22, 53}, DNS: true}, "unifi-gateway"},
		{"non-ubiquiti mac stays ssh", Fingerprint{MAC: "aa:bb:cc:dd:ee:ff", TCP: []int{22}}, "linux-ssh"},
		{"windows", Fingerprint{SysDescr: "Hardware: Intel64 ... Software: Windows Version 6.3"}, "windows-snmp"},
		// Specific identities beat service signals - a Windows DNS server stays Windows.
		{"windows dns server", Fingerprint{SysDescr: "Hardware: Intel64 ... Software: Windows Version 6.3", TCP: []int{53}, DNS: true}, "windows-snmp"},
		// ... but a live service beats the GENERIC Linux guess: a Debian box answering real DNS
		// queries (AdGuard without a page title) or serving upsd is that service first.
		{"linux running dns", Fingerprint{SysDescr: "Linux adguard1 6.12.107+deb13-amd64", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10", TCP: []int{22, 53, 80}, DNS: true}, "dns-server"},
		{"linux running nut", Fingerprint{SysDescr: "Linux ups-pi 6.6.134+ aarch64", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10", TCP: []int{22, 3493}}, "nut-collector"},
		{"linux", Fingerprint{SysDescr: "Linux storage1 6.1.0 x86_64", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, "linux-snmp"},
		{"unraid", Fingerprint{SysDescr: "Linux Tower 6.12.10-Unraid x86_64", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, "unraid"},
		{"ugreen", Fingerprint{SysDescr: "Linux nas 5.x", SysName: "UGOS-NAS", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, "ugreen"},
		// An SNMP vendor we can't place (SwOS, printers): the Linux template would be all wrong,
		// so no suggestion (plain Ping) - lab-confirmed on a MikroTik SwOS switch.
		{"unknown snmp -> base", Fingerprint{SysDescr: "CRS305-1G-4S+ SwOS v2.18", SysObjectID: ".1.3.6.1.4.1.14988.2", TCP: []int{80}}, ""},
		{"dns server, no snmp", Fingerprint{TCP: []int{53, 22}, DNS: true}, "dns-server"},
		{"nut", Fingerprint{TCP: []int{3493}}, "nut-collector"},
		{"ssh only", Fingerprint{TCP: []int{22}}, "linux-ssh"},
		{"web only -> base", Fingerprint{TCP: []int{443}}, ""},
		{"nothing -> base", Fingerprint{}, ""},
	}
	for _, c := range cases {
		if got := SuggestClass(c.f); got != c.want {
			t.Errorf("%s: SuggestClass = %q, want %q", c.name, got, c.want)
		}
	}
	// Every suggested id must exist in the registry, so the review UI's Combobox can preselect it.
	for _, c := range cases {
		if c.want == "" {
			continue
		}
		if _, ok := ClassByID(c.want); !ok {
			t.Errorf("%s: suggested class %q is not in the registry", c.name, c.want)
		}
	}
}

// Every class offers the HTTP/HTTPS add-on: any device may expose a web UI worth watching, and the
// discovery review + Add-device forms rely on the toggle always being available (user's call).
func TestEveryClassOffersHTTP(t *testing.T) {
	for _, c := range Classes() {
		if !c.OffersHTTP {
			t.Errorf("class %q does not offer the HTTP/HTTPS add-on - every class should", c.ID)
		}
	}
}
