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
		{"unifi ap", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.6", SysDescr: "UAP-AC-Pro 6.6.55"}, "unifi-ap"},
		{"unifi switch", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.5", SysDescr: "USW-24-PoE"}, "unifi-switch"},
		{"unifi gateway", Fingerprint{SysObjectID: ".1.3.6.1.4.1.41112.1.4", SysName: "UDM-Pro"}, "unifi-gateway"},
		{"windows", Fingerprint{SysDescr: "Hardware: Intel64 ... Software: Windows Version 6.3"}, "windows-snmp"},
		{"linux", Fingerprint{SysDescr: "Linux storage1 6.1.0 x86_64", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, "linux-snmp"},
		{"ugreen", Fingerprint{SysDescr: "Linux nas 5.x", SysName: "UGOS-NAS", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, "ugreen"},
		{"unknown snmp still worth snmp", Fingerprint{SysDescr: "JetDirect ..."}, "linux-snmp"},
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
