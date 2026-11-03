// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/store"
)

func TestCleanAddr(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.1.20":         "10.0.1.20",
		" 203.0.113.10\n":   "203.0.113.10",
		"::ffff:10.0.1.20":  "10.0.1.20", // a v4-mapped peer address
		"2001:db8::1":       "2001:db8::1",
		"127.0.0.1":         "",
		"0.0.0.0":           "",
		"169.254.10.1":      "",
		"fe80::1%eth0":      "",
		"224.0.0.1":         "",
		"10.0.1.20:8443":    "",
		"proxy.example.lan": "",
		"":                  "",
	} {
		if got := cleanAddr(in); got != want {
			t.Errorf("cleanAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// Which LAN and public address the Probes page shows, for the ways probes run.
func TestProbeAddrChoice(t *testing.T) {
	cases := []struct {
		name           string
		a              store.ProbeAddrs
		lan, lanSrc    string
		public, pubSrc string
	}{
		{"probe VM: the host's report wins", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.1.20", Host: "10.0.1.20", HostGW: "10.0.1.1", Public: "203.0.113.10"},
			"10.0.1.20", "host", "203.0.113.10", "probe"},
		{"container on a bridge, report over the VPN: the address Argus sees", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.2.15", Public: "198.51.100.24"},
			"10.0.2.15", "seen", "198.51.100.24", "probe"},
		{"container on a bridge, report over the internet: no LAN address", store.ProbeAddrs{Own: "172.17.0.2", Seen: "198.51.100.40", Public: "198.51.100.40"},
			"", "", "198.51.100.40", "probe"},
		{"host networking over the internet (the field kit): its own address", store.ProbeAddrs{Own: "192.168.5.23", Seen: "198.51.100.41"},
			"192.168.5.23", "probe", "198.51.100.41", "seen"},
		{"a site on 172.16/12 seen as itself: its own address", store.ProbeAddrs{Own: "172.20.1.5", Seen: "172.20.1.5"},
			"172.20.1.5", "probe", "", ""},
		{"macvlan through a NATing VPN: its own address", store.ProbeAddrs{Own: "10.0.3.30", Seen: "10.99.0.2"},
			"10.0.3.30", "probe", "", ""},
		{"a Tailscale address counts as private", store.ProbeAddrs{Own: "172.17.0.2", Seen: "100.101.102.103"},
			"100.101.102.103", "seen", "", ""},
		{"nothing reported yet", store.ProbeAddrs{}, "", "", "", ""},
	}
	for _, c := range cases {
		lan, pub := probeLAN(c.a), probePublic(c.a)
		gotLAN, gotLANSrc, gotPub, gotPubSrc := "", "", "", ""
		if lan != nil {
			gotLAN, gotLANSrc = lan.IP, lan.Src
		}
		if pub != nil {
			gotPub, gotPubSrc = pub.IP, pub.Src
		}
		if gotLAN != c.lan || gotLANSrc != c.lanSrc || gotPub != c.public || gotPubSrc != c.pubSrc {
			t.Errorf("%s: LAN %q (%s), public %q (%s); want LAN %q (%s), public %q (%s)",
				c.name, gotLAN, gotLANSrc, gotPub, gotPubSrc, c.lan, c.lanSrc, c.public, c.pubSrc)
		}
	}
}
