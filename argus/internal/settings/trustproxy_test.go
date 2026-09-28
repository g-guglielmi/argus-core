// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package settings

import "testing"

func TestParseTrustProxy(t *testing.T) {
	for in, want := range map[string]string{"": "", "false": "", "TRUE": "true", "yes": "true",
		"10.0.0.2, 10.0.5.0/24": "10.0.0.2/32, 10.0.5.0/24", "10.0.5.9/24;fd00::1": "10.0.5.0/24, fd00::1/128"} {
		_, got, err := ParseTrustProxy(in)
		if err != nil || got != want {
			t.Errorf("ParseTrustProxy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, _, err := ParseTrustProxy("10.0.0.2, haproxy"); err == nil {
		t.Error("a name was accepted as a proxy network")
	}
}

func TestTrustProxyClientIP(t *testing.T) {
	off, _, _ := ParseTrustProxy("")
	one, _, _ := ParseTrustProxy("true")
	chain, _, _ := ParseTrustProxy("10.0.0.2, 10.0.5.0/24") // HAProxy 10.0.0.2 behind NetScaler 10.0.5.x

	cases := []struct {
		name   string
		t      TrustProxy
		remote string
		xff    []string
		want   string
	}{
		{"no proxy ignores the header", off, "203.0.113.7:5000", []string{"1.2.3.4"}, "203.0.113.7"},
		{"one proxy: the entry it appended", one, "10.0.0.2:5000", []string{"6.6.6.6, 203.0.113.7"}, "203.0.113.7"},
		{"one proxy: a separate header line", one, "10.0.0.2:5000", []string{"6.6.6.6", "203.0.113.7"}, "203.0.113.7"},
		{"one proxy: no header", one, "10.0.0.2:5000", nil, "10.0.0.2"},
		{"chain: skip our proxies from the right", chain, "10.0.0.2:5000", []string{"6.6.6.6, 203.0.113.7, 10.0.5.20"}, "203.0.113.7"},
		{"chain: an untrusted peer's header is ignored", chain, "198.51.100.1:5000", []string{"10.0.0.99"}, "198.51.100.1"},
		{"chain: every hop is ours", chain, "10.0.0.2:5000", []string{"10.0.5.20"}, "10.0.5.20"},
		{"chain: garbage means the peer is the client", chain, "10.0.0.2:5000", []string{"203.0.113.7, unknown"}, "10.0.0.2"},
		{"one proxy: garbage means the peer is the client", one, "10.0.0.2:5000", []string{"not-an-ip"}, "10.0.0.2"},
		{"one proxy: a public peer is a direct client", one, "198.51.100.1:5000", []string{"1.2.3.4"}, "198.51.100.1"},
	}
	for _, c := range cases {
		if got := c.t.ClientIP(c.remote, c.xff); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if !chain.Trusts("10.0.5.3:1") || chain.Trusts("10.0.6.3:1") || off.Trusts("10.0.0.2:1") {
		t.Error("Trusts (list)")
	}
	// "true" believes a proxy beside Argus (private, loopback), never a public peer.
	if !one.Trusts("10.0.0.2:1") || !one.Trusts("127.0.0.1:1") || !one.Trusts("[::1]:1") || one.Trusts("198.51.100.1:1") {
		t.Error("Trusts (true)")
	}
}
