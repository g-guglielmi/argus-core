// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"
	"time"

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

// Which LAN and public address the Probes page shows, for the ways probes run. The address Argus
// sees is never the LAN address: any NAT on the way puts its own there.
func TestProbeAddrChoice(t *testing.T) {
	cases := []struct {
		name                 string
		a                    store.ProbeAddrs
		lan, lanSrc, lanNone string
		public, pubSrc       string
	}{
		{"probe VM, or a sidecar's host report: the host's address wins", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.1.20", Host: "10.0.1.20", HostGW: "10.0.1.1", Public: "203.0.113.10"},
			"10.0.1.20", "host", "", "203.0.113.10", "probe"},
		{"container on a bridge without a sidecar: no LAN address, not the one Argus sees", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.2.15", Public: "198.51.100.24"},
			"", "", "bridge", "198.51.100.24", "probe"},
		{"a VPS through a Tailscale exit node: no LAN, its host's own address is public", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.0.2", Host: "198.51.100.50"},
			"", "", "public_host", "198.51.100.50", "host"},
		{"the VPS's own lookup still comes first", store.ProbeAddrs{Own: "172.17.0.2", Seen: "10.0.0.2", Host: "198.51.100.50", Public: "198.51.100.50"},
			"", "", "public_host", "198.51.100.50", "probe"},
		{"container on a bridge, report over the internet", store.ProbeAddrs{Own: "172.17.0.2", Seen: "198.51.100.40"},
			"", "", "bridge", "198.51.100.40", "seen"},
		{"host networking over the internet (the field kit): its own address", store.ProbeAddrs{Own: "192.168.5.23", Seen: "198.51.100.41"},
			"192.168.5.23", "probe", "", "198.51.100.41", "seen"},
		{"a site on 172.16/12 seen as itself: its own address", store.ProbeAddrs{Own: "172.20.1.5", Seen: "172.20.1.5"},
			"172.20.1.5", "probe", "", "", ""},
		{"macvlan through a NATing VPN: its own address", store.ProbeAddrs{Own: "10.0.3.30", Seen: "10.99.0.2"},
			"10.0.3.30", "probe", "", "", ""},
		{"a Tailscale address on the host counts as private", store.ProbeAddrs{Own: "172.17.0.2", Host: "100.101.102.103"},
			"100.101.102.103", "host", "", "", ""},
		{"a macvlan (a dedicated address on Unraid's br0): its own address, not the host's", store.ProbeAddrs{Own: "10.0.4.250", Seen: "10.0.4.250", Host: "10.0.4.10", Net: "macvlan"},
			"10.0.4.250", "probe", "", "", ""},
		{"an ipvlan seen through a NAT: still its own address", store.ProbeAddrs{Own: "10.0.5.250", Seen: "10.0.0.2", Host: "10.0.5.10", Net: "ipvlan"},
			"10.0.5.250", "probe", "", "", ""},
		{"a dedicated address in 172.16/12 on a macvlan: its own address", store.ProbeAddrs{Own: "172.20.1.5", Seen: "10.0.0.2", Net: "macvlan"},
			"172.20.1.5", "probe", "", "", ""},
		{"a dedicated address, no sidecar: its own address over the host's", store.ProbeAddrs{Own: "10.0.6.250", Seen: "10.0.6.250", Host: "10.0.6.10"},
			"10.0.6.250", "probe", "", "", ""},
		{"a user's bridge handing out 192.168.x: the host's", store.ProbeAddrs{Own: "192.168.100.2", Seen: "10.0.7.10", Host: "10.0.7.10", Net: "bridge"},
			"10.0.7.10", "host", "", "", ""},
		{"nothing reported yet", store.ProbeAddrs{}, "", "", "", "", ""},
	}
	for _, c := range cases {
		lan, why := probeLAN(c.a)
		pub := probePublic(c.a)
		gotLAN, gotLANSrc, gotPub, gotPubSrc := "", "", "", ""
		if lan != nil {
			gotLAN, gotLANSrc = lan.IP, lan.Src
		}
		if pub != nil {
			gotPub, gotPubSrc = pub.IP, pub.Src
		}
		if gotLAN != c.lan || gotLANSrc != c.lanSrc || why != c.lanNone || gotPub != c.public || gotPubSrc != c.pubSrc {
			t.Errorf("%s: LAN %q (%s, none %q), public %q (%s); want LAN %q (%s, none %q), public %q (%s)",
				c.name, gotLAN, gotLANSrc, why, gotPub, gotPubSrc, c.lan, c.lanSrc, c.lanNone, c.public, c.pubSrc)
		}
	}
}

// The public address's age comes from the probe; an absent or absurd one means just now.
func TestPublicFoundAt(t *testing.T) {
	now := time.Unix(1700000000, 0)
	age := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		age  *int64
		want int64
	}{
		{nil, 1700000000},
		{age(0), 1700000000},
		{age(1200), 1699998800},
		{age(-5), 1700000000},
		{age(31 * 86400), 1700000000},
	} {
		if got := publicFoundAt(c.age, now); got != c.want {
			t.Errorf("publicFoundAt(%v) = %d, want %d", c.age, got, c.want)
		}
	}
}
