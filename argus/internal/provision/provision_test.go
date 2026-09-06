package provision

import (
	"strings"
	"testing"
)

// The embedded templates load, hash stably, and cover the universal Base Ping + HTTP add-on that C0
// ships. (Zabbix-schema validity is confirmed by the lab import; this guards syntax + presence.)
func TestLoadTemplates(t *testing.T) {
	docs, h1, err := loadTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) < 2 {
		t.Fatalf("expected at least the base + http templates, got %d", len(docs))
	}
	if h1 == "" {
		t.Fatal("empty hash")
	}
	_, h2, _ := loadTemplates()
	if h1 != h2 {
		t.Fatalf("hash not stable: %s vs %s", h1, h2)
	}

	all := ""
	for _, d := range docs {
		all += d.content
	}
	for _, want := range []string{TemplateBasePing, TemplateHTTP, "Argus Linux by SNMP", "icmpping", "{$HTTP.PORT}", "{$PING.LOSS.WARN}", "{$CPU.UTIL.WARN}"} {
		if !strings.Contains(all, want) {
			t.Errorf("templates missing %q", want)
		}
	}
}

func TestRegistry(t *testing.T) {
	if len(Classes()) == 0 {
		t.Fatal("empty registry")
	}
	base, ok := ClassByID("base")
	if !ok {
		t.Fatal("base class missing")
	}
	if base.Pattern != PatternBase || base.Iface != IfaceAgent || !base.OffersHTTP {
		t.Fatalf("unexpected base class: %+v", base)
	}
	// The Generic Linux SNMP class (C1) drives the SNMP interface branch of the create path.
	lx, ok := ClassByID("linux-snmp")
	if !ok {
		t.Fatal("linux-snmp class missing")
	}
	if lx.Pattern != PatternSNMP || lx.Iface != IfaceSNMP || len(lx.Templates) != 1 {
		t.Fatalf("unexpected linux-snmp class: %+v", lx)
	}
	if _, ok := ClassByID("does-not-exist"); ok {
		t.Fatal("unexpected class")
	}
}
