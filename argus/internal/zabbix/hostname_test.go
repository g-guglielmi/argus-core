// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"strings"
	"testing"
)

func TestTechnicalName(t *testing.T) {
	cases := map[string]string{
		"351 - U6+ Salotto":   "351 - U6_ Salotto",
		"sw-core.example.lan": "sw-core.example.lan",
		"Città (piano 2)":     "Citta _piano 2_",
		"AP #3 / Cucina":      "AP _3 _ Cucina",
		"  NAS  ":             "NAS",
		"+++":                 "host",
		"日本":                  "host",
	}
	for in, want := range cases {
		got := TechnicalName(in)
		if got != want {
			t.Errorf("TechnicalName(%q) = %q, want %q", in, got, want)
		}
		if !ValidTechnicalName(got) {
			t.Errorf("TechnicalName(%q) = %q is not a valid technical name", in, got)
		}
	}
	if got := TechnicalName(strings.Repeat("a", 200)); len(got) != 128 {
		t.Errorf("long name not capped: %d", len(got))
	}
	for _, bad := range []string{"", "a+b", " lead", "trail ", strings.Repeat("x", 129)} {
		if ValidTechnicalName(bad) {
			t.Errorf("ValidTechnicalName(%q) = true", bad)
		}
	}
}

func TestItemUnits(t *testing.T) {
	if got := itemUnits("Number of processed values per second", "!vps"); got != "vps" {
		t.Errorf("Zabbix's no-prefix marker should be dropped: %q", got)
	}
	if got := itemUnits("Zabbix server: Proxy [site1]: Last seen, in seconds", ""); got != "s" {
		t.Errorf("unitless seconds counter: %q", got)
	}
	if got := itemUnits("Free disk space", ""); got != "" {
		t.Errorf("unrelated item: %q", got)
	}
	if got := itemUnits("Uptime, in seconds", "uptime"); got != "uptime" {
		t.Errorf("explicit units must win: %q", got)
	}
}
