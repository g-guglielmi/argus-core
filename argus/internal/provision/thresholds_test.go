// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import "testing"

// TestTemplateFactoryDefaults checks the dependency-free YAML scanner reads real factory values out of
// the embedded templates - a canary for a format drift in the templates' macros blocks.
func TestTemplateFactoryDefaults(t *testing.T) {
	defs, err := TemplateFactoryDefaults()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	cases := []struct{ tpl, macro, want string }{
		{"Argus Linux by SNMP", "{$CPU.UTIL.WARN}", "80"},
		{"Argus Linux by SNMP", "{$DISK.PUSED.HIGH}", "95"},
		{"Argus Base Ping", "{$PING.RESP.WARN}", "0.15"},
		{"Argus NAS by Zabbix agent", "{$DISK.TEMP.WARN:ssd}", "65"},
		{"Argus unRAID by SNMP", "{$POOL.TEMP.HIGH}", "75"},
		{"Argus XCP-NG by XAPI", "{$XCP.CPU.UTIL.WARN}", "85"},
		{"Argus XCP-NG by XAPI", "{$XCP.CPU.UTIL.HIGH}", "95"},
		{"Argus Base Ping", "{$PING.LOSS.HIGH}", "60"},
		{"Argus UniFi Gateway by HTTP", "{$UNIFI.WAN.AVAIL.MIN}", "50"},
	}
	for _, c := range cases {
		if got := defs[c.tpl][c.macro]; got != c.want {
			t.Errorf("%s %s = %q, want %q", c.tpl, c.macro, got, c.want)
		}
	}
}

// TestThresholdCatalogMatchesTemplates asserts every catalog macro exists in its template's YAML with
// a non-empty factory value - so a typo in the catalog, or a macro renamed in a template, fails CI.
func TestThresholdCatalogMatchesTemplates(t *testing.T) {
	defs, err := TemplateFactoryDefaults()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, tt := range ThresholdCatalog() {
		tv, ok := defs[tt.Template]
		if !ok {
			t.Errorf("catalog template %q not found in the embedded YAML", tt.Template)
			continue
		}
		if len(tt.Specs) == 0 {
			t.Errorf("catalog template %q has no threshold specs", tt.Template)
		}
		for _, sp := range tt.Specs {
			if v, ok := tv[sp.Macro]; !ok || v == "" {
				t.Errorf("catalog macro %s missing/empty in template %q (found=%v value=%q)", sp.Macro, tt.Template, ok, v)
			}
		}
	}
}

// TestThresholdsForTemplates returns only the requested templates' catalog entries.
func TestThresholdsForTemplates(t *testing.T) {
	got := ThresholdsForTemplates([]string{TemplateBasePing, "Argus Linux by SNMP", "Argus Nonexistent"})
	if len(got) != 2 {
		t.Fatalf("expected 2 matched templates, got %d", len(got))
	}
	for _, g := range got {
		if g.Template != TemplateBasePing && g.Template != "Argus Linux by SNMP" {
			t.Errorf("unexpected template %q", g.Template)
		}
	}
}
