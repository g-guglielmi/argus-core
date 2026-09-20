// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

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
