// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"reflect"
	"testing"
)

// A host's web checks name its domains: full URLs and bare host names, by the domain a registry
// sells; paths, addresses and internal names name none.
func TestURLDomains(t *testing.T) {
	got := urlDomains("https://shop.example.com/app#text=Welcome, portal.example.co.uk:8443 /login https://10.0.0.20/ nas.example.lan https://www.example.com/x http://EXAMPLE.it")
	want := []string{"example.com", "example.co.uk", "example.it"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Days left are whole days: the last day reads 0, the day after it -1.
func TestDaysLeft(t *testing.T) {
	now := int64(1_000_000_000)
	for at, want := range map[int64]int64{now + 30*86400: 30, now + 30*86400 - 1: 29, now + 3600: 0, now: 0, now - 1: -1, now - 86400: -1, now - 86401: -2} {
		if got := daysLeft(at, now); got != want {
			t.Errorf("at now%+d: got %d, want %d", at-now, got, want)
		}
	}
}
