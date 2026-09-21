// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import "strings"

// Fingerprint is the raw fact set a discovery scan reports for one host (§B). The scanner on the
// probe collects facts only; mapping them to a device class happens here on the core, so the
// mapping can grow without touching the fleet.
type Fingerprint struct {
	SysDescr     string // SNMP sysDescr ("" = no SNMP answer)
	SysObjectID  string // SNMP sysObjectID, dotted with leading "."
	SysName      string // SNMP sysName
	HTTPTitle    string // <title> of the first answering web port (redirects followed same-host)
	HTTPServer   string // Server response header
	HTTPLocation string // Location header of a redirecting / ("" otherwise)
	DNS          bool   // the host answered a real DNS query on udp/53
	TCP          []int  // open TCP ports from the scanner's probe set
	MAC          string // MAC address (host-network probe scans on the same L2 only; "" otherwise)
	RDNS         string // reverse-DNS name ("" if none)
	SSHBanner    string // the SSH server's version banner, e.g. "SSH-2.0-dropbear_2022.83"
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

	// Product pages are the most specific signal. "XO Lite" is the page an XCP-NG host itself
	// serves on :443, and a UniFi OS console titles its login "UniFi OS"/"UniFi Network"
	// (both lab-confirmed - the devices never name their product line in sysDescr).
	switch {
	case strings.Contains(title, "adguard home"):
		return "adguard"
	case strings.Contains(title, "home assistant"):
		return "home-assistant"
	case strings.Contains(title, "xcp-ng") || strings.Contains(title, "xenserver") || strings.Contains(title, "xo lite"):
		return "xcpng"
	// "UniFi OS" is what a gateway serves (UDM/UCG/UXG all run UniFi OS); a self-hosted Network
	// Server (the Debian-VM console) titles its login "UniFi Network".
	case strings.Contains(title, "unifi os"):
		return "unifi-gateway"
	case strings.Contains(title, "unifi network"):
		return "unifi-console"
	case strings.Contains(title, "unraid"):
		return "unraid"
	}

	// Hostname hints: an rDNS or sysName that NAMES the product is trusted - the admin called the
	// box after what it runs, and that beats the OS identity (a Debian VM named "AdGuard" should be
	// monitored as AdGuard, not as generic Linux).
	nameIdent := strings.ToLower(f.RDNS + " " + f.SysName)
	switch {
	case strings.Contains(nameIdent, "adguard"):
		return "adguard"
	case strings.Contains(nameIdent, "homeassistant") || strings.Contains(nameIdent, "home-assistant"):
		return "home-assistant"
	case strings.Contains(nameIdent, "pihole") || strings.Contains(nameIdent, "pi-hole"):
		return "dns-server"
	}
	// AdGuard's / redirects to /login.html (a giveaway even when the scanner didn't follow it to
	// the titled page) - together with a live DNS answer that's AdGuard.
	if f.DNS && strings.HasPrefix(strings.ToLower(f.HTTPLocation), "/login.html") {
		return "adguard"
	}

	uiMAC := ouiUbiquiti(f.MAC)

	// SNMP answered: vendor/OS identification.
	if f.SysDescr != "" || f.SysObjectID != "" {
		ident := descr + " " + sysname
		// UniFi: current firmware reports Ubiquiti's enterprise OID, but older firmware answers
		// with the stock net-snmp OID and reveals itself only through "UBNT" in sysDescr or a
		// model token in sysDescr/sysName (lab-confirmed on a USW SFP and a garage US-8-60W whose
		// only hint was "US-8-60W" + "USW8" in the name) - so a STRONG model token opens this
		// branch on its own, before the plain "linux" check below can swallow it.
		strong := unifiModelClass(ident)
		isUbnt := strings.HasPrefix(f.SysObjectID, oidUbiquiti) || containsAny(ident, "ubnt", "unifi") || uiMAC
		if strong != "" {
			return strong
		}
		if isUbnt {
			// Confirmed Ubiquiti - weaker hints are safe to read now.
			switch {
			case strings.Contains(ident, "gateway"):
				return "unifi-gateway"
			case containsAny(ident, "ac-", "access point"):
				return "unifi-ap"
			}
			if strings.HasPrefix(f.SysObjectID, oidUbiquiti) {
				return "unifi-ap" // most UniFi SNMP responders with no model token are APs
			}
			// "ubnt"/"unifi" seen but nothing else: fall through to the OS checks.
		}
		switch {
		case strings.Contains(descr, "windows"):
			return "windows-snmp"
		case containsAny(ident, "ugos", "ugreen"):
			return "ugreen"
		case strings.Contains(descr, "unraid"):
			return "unraid"
		// A live service outranks the GENERIC Linux guess (a Pi answering real DNS queries or
		// serving upsd is better monitored as that service; specific identities above still win):
		case f.DNS:
			return "dns-server"
		case hasPort(f.TCP, 3493):
			return "nut-collector"
		case strings.Contains(descr, "linux"):
			return "linux-snmp"
		}
		// An SNMP answer from a vendor we can't place (MikroTik SwOS, printers, ...): the Linux
		// SNMP template would be all wrong for it, so suggest plain Ping and let the admin pick.
		return ""
	}

	// No SNMP, but the MAC's OUI says Ubiquiti (probe scans see the L2 address): the SNMP-less
	// UniFi models are almost all the small switches (USW-Flex/Ultra ship no SNMP agent) - and a
	// UniFi device answering DNS is the gateway. Best-effort, easily overridden in review.
	if uiMAC {
		if f.DNS {
			return "unifi-gateway"
		}
		return "unifi-switch"
	}

	// No SNMP: fall back to service ports.
	if f.DNS {
		return "dns-server"
	}
	if hasPort(f.TCP, 3493) {
		return "nut-collector"
	}
	if hasPort(f.TCP, 22) {
		// dropbear = embedded gear (UniFi switches, routers): busybox lacks the df/proc idioms the
		// SSH collector needs, so the Linux (SSH) class can't work there - plain Ping instead.
		if strings.Contains(strings.ToLower(f.SSHBanner), "dropbear") {
			return ""
		}
		return "linux-ssh"
	}
	return "" // base Ping (the review UI pre-ticks the HTTP add-on when a web port answered)
}

// ubiquitiOUIs are Ubiquiti's IEEE-registered MAC prefixes (lowercase, no separators) - a curated
// set of the well-known ones, so a device that answers nothing but SSH can still be identified by
// its L2 address. Best-effort: a brand-new OUI just won't match until added here.
var ubiquitiOUIs = map[string]bool{
	"00156d": true, "002722": true, "0418d6": true, "18e829": true, "245a4c": true,
	"24a43c": true, "28704e": true, "44d9e7": true, "602232": true, "687251": true,
	"68d79a": true, "70a741": true, "7483c2": true, "74acb9": true, "784558": true,
	"788a20": true, "802aa8": true, "942a6f": true, "9c05d6": true, "ac8ba9": true,
	"b4fbe4": true, "d021f9": true, "d8b370": true, "dc9fdb": true, "e063da": true,
	"e43883": true, "f09fc2": true, "f492bf": true, "f4e2c6": true, "fcecda": true,
	"245ebe": true, "6083e7": true, "1c6a1b": true, "e438f2": true, "accb51": true,
}

// ouiUbiquiti reports whether a MAC address (any separator style) carries a Ubiquiti OUI.
func ouiUbiquiti(mac string) bool {
	m := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			return r
		case r >= 'A' && r <= 'F':
			return r + ('a' - 'A')
		}
		return -1
	}, mac)
	return len(m) >= 6 && ubiquitiOUIs[m[:6]]
}

// unifiModelClass maps STRONG UniFi model tokens in the lowercased SNMP identity to a class -
// tokens specific enough to identify Ubiquiti gear on their own, without the enterprise OID
// (generic words like "gateway" are NOT here; they only count once Ubiquiti is confirmed).
func unifiModelClass(ident string) string {
	switch {
	case containsAny(ident, "usw", "us-8", "us-16", "us-24", "us-48", "edgeswitch", "unifi switch"):
		return "unifi-switch"
	case containsAny(ident, "ugw", "usg", "udm", "uxg", "ucg"):
		return "unifi-gateway"
	case containsAny(ident, "uap", "u6", "u7", "nanohd"):
		return "unifi-ap"
	}
	return ""
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
