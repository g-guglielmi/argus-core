// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"strings"
	"testing"
)

// The Common SaaS template starts with exactly the catalog's default services, each with its name, and
// the list passes the add-on's own check.
func TestSaaSDefaults(t *testing.T) {
	def, err := TemplateFactoryDefaults()
	if err != nil {
		t.Fatal(err)
	}
	got := def[TemplateSaaS]["{$SAAS.URLS}"]
	if got != DefaultSaaSURLs() {
		t.Fatalf("template default differs from the catalog:\n%s\n%s", got, DefaultSaaSURLs())
	}
	if !strings.Contains(got, "#name=Microsoft%20365") {
		t.Fatalf("names: %s", got)
	}
	if err := checkURLList(got); err != nil {
		t.Fatal(err)
	}
	if err := checkURLList("https://example.com/#name="); err == nil {
		t.Fatal("an empty name passed")
	}
	a, ok := AddOnByID("speedtest")
	if !ok || a.OffersOn("linux-snmp", false) || !a.OffersOn(ClassProbe, true) {
		t.Fatal("the speedtest is a probe add-on")
	}
	if h, _ := AddOnByID("http"); h.OffersOn(ClassProbe, true) || !h.OffersOn("linux-snmp", false) {
		t.Fatal("the HTTP add-on is for devices, not the probe host")
	}
}
