// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// The limiters key an IPv6 client by its /64: a residential prefix is one client, not 2^64.
func TestLimiterAddr(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.7":                      "10.0.0.7",
		"::ffff:10.0.0.7":               "10.0.0.7",
		"2001:db8:1:2:aaaa:bbbb:cccc:1": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:0:0:2":       "2001:db8:1:2::/64",
		"not an address":                "not an address",
	} {
		if got := limiterAddr(in); got != want {
			t.Errorf("limiterAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
