// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import "strings"

// Fingerprint is the raw fact set a discovery scan reports for one host (§B). The scanner on the
// probe collects facts only; mapping them to a device class happens here on the core, so the
// mapping can grow without touching the fleet.
type Fingerprint struct {
	SysDescr    string // SNMP sysDescr ("" = no SNMP answer)
	SysObjectID string // SNMP sysObjectID, dotted with leading "."
	SysName     string // SNMP sysName
	HTTPTitle   string // <title> of the first answering web port
	HTTPServer  string // Server response header
	DNS         bool   // the host answered a real DNS query on udp/53
	TCP         []int  // open TCP ports from the scanner's probe set
}

// oidUbiquiti is the Ubiquiti enterprise arc (UniFi devices).
const oidUbiquiti = ".1.3.6.1.4.1.41112"

// SuggestClass proposes a device class for a scanned host - best-effort, the review UI lets the
// admin override it. "" means no suggestion beyond the base Ping class. Ordering matters: an HTTP
// title identifies a product exactly, SNMP identifies the OS/vendor, and open ports are the
// weakest hint.
func SuggestClass(f Fingerprint) string {
	title := strings.ToLower(f.HTTPTitle)
	descr := strings.ToLower(f.SysDescr)
	sysname := strings.ToLower(f.SysName)

	// Product pages are the most specific signal.
	switch {
	case strings.Contains(title, "adguard home"):
		return "adguard"
	case strings.Contains(title, "home assistant"):
		return "home-assistant"
	case strings.Contains(title, "xcp-ng") || strings.Contains(title, "xenserver"):
		return "xcpng"
	case strings.Contains(title, "unraid"):
		return "unraid"
	}

	// SNMP answered: vendor/OS identification.
	if f.SysDescr != "" || f.SysObjectID != "" {
		if strings.HasPrefix(f.SysObjectID, oidUbiquiti) {
			ident := descr + " " + sysname
			switch {
			case containsAny(ident, "usw", "us-", "unifi switch"):
				return "unifi-switch"
			case containsAny(ident, "ugw", "usg", "udm", "uxg", "ucg", "gateway"):
				return "unifi-gateway"
			case containsAny(ident, "uap", "u6", "u7", "ac-", "access point"):
				return "unifi-ap"
			}
			return "unifi-ap" // most UniFi SNMP responders are APs; easily overridden in review
		}
		switch {
		case strings.Contains(descr, "windows"):
			return "windows-snmp"
		case containsAny(descr+" "+sysname, "ugos", "ugreen"):
			return "ugreen"
		case strings.Contains(descr, "unraid"):
			return "unraid"
		case strings.Contains(descr, "linux"):
			return "linux-snmp"
		}
		// An SNMP answer we can't place is still worth an SNMP class over plain ping.
		return "linux-snmp"
	}

	// No SNMP: fall back to service ports.
	if f.DNS {
		return "dns-server"
	}
	if hasPort(f.TCP, 3493) {
		return "nut-collector"
	}
	if hasPort(f.TCP, 22) {
		return "linux-ssh"
	}
	return "" // base Ping (the review UI pre-ticks the HTTP add-on when a web port answered)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hasPort(ports []int, p int) bool {
	for _, x := range ports {
		if x == p {
			return true
		}
	}
	return false
}
