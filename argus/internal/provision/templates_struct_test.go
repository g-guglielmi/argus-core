// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package provision

import (
	"strings"
	"testing"
)

// Zabbix refuses a whole template file for one field in the wrong place (a discovery rule with an
// item's "history"), and the import stops there, so every template after it alphabetically keeps its
// old version. Each entry of a template's items or discovery rules must only carry fields of its kind.
func TestTemplateSectionsCarryTheirOwnFields(t *testing.T) {
	itemOnly := map[string]bool{"history": true, "trends": true, "value_type": true, "units": true, "valuemap": true, "inventory_link": true, "triggers": true}
	ruleOnly := map[string]bool{"item_prototypes": true, "trigger_prototypes": true, "graph_prototypes": true, "host_prototypes": true, "filter": true, "lld_macro_paths": true, "lifetime": true, "lifetime_type": true, "enabled_lifetime_type": true, "overrides": true}
	docs, _, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		section, key := "", ""
		for n, line := range strings.Split(strings.ReplaceAll(d.content, "\r\n", "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") && strings.HasSuffix(line, ":"):
				section = strings.TrimSuffix(strings.TrimSpace(line), ":") // items, discovery_rules, macros, ...
			case strings.HasPrefix(line, "          key: ") && !strings.HasPrefix(line, "           "):
				key = strings.TrimPrefix(line, "          key: ")
			case strings.HasPrefix(line, "          ") && !strings.HasPrefix(line, "           ") && strings.Contains(line, ":"):
				field := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
				if section == "discovery_rules" && itemOnly[field] {
					t.Errorf("%s line %d: discovery rule %s has an item's %q (an item put among the rules?)", d.name, n+1, key, field)
				}
				if section == "items" && ruleOnly[field] {
					t.Errorf("%s line %d: item %s has a discovery rule's %q", d.name, n+1, key, field)
				}
			}
		}
	}
}

// templateItemKeys reads every template's item keys (items, not prototypes), by template name.
func templateItemKeys(t *testing.T) map[string][]string {
	t.Helper()
	docs, _, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, d := range docs {
		name, section := "", ""
		for _, line := range strings.Split(strings.ReplaceAll(d.content, "\r\n", "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "      template: "):
				name = strings.Trim(strings.TrimPrefix(line, "      template: "), "'")
			case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") && strings.HasSuffix(line, ":"):
				section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			case section == "items" && strings.HasPrefix(line, "          key: ") && !strings.HasPrefix(line, "           "):
				out[name] = append(out[name], strings.Trim(strings.TrimPrefix(line, "          key: "), "'"))
			}
		}
	}
	return out
}

// templateAllKeys reads every key a template brings to a host: its items, its discovery rules and
// their item prototypes, which share one namespace on a host. By template name.
func templateAllKeys(t *testing.T) map[string]map[string]bool {
	t.Helper()
	docs, _, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	for _, d := range docs {
		name := ""
		for _, line := range strings.Split(strings.ReplaceAll(d.content, "\r\n", "\n"), "\n") {
			if strings.HasPrefix(line, "      template: ") {
				name = strings.Trim(strings.TrimPrefix(line, "      template: "), "'")
				out[name] = map[string]bool{}
				continue
			}
			tl := strings.TrimPrefix(strings.TrimLeft(line, " "), "- ")
			if name != "" && strings.HasPrefix(tl, "key: ") {
				out[name][strings.Trim(strings.TrimPrefix(tl, "key: "), "'")] = true
			}
		}
	}
	return out
}

// Sets a host may carry together share nothing: Zabbix refuses a host that would inherit one key
// (an item, a discovery rule or an item prototype) from two templates, and a macro two linked
// templates both define has no certain value, so a threshold could follow the wrong one. Two sets
// that read the same sensors another way share a slot instead, and a host carries one of a slot.
// A new template that fails here needs a slot, or its own keys and macro names.
func TestCombinableSetsShareNothing(t *testing.T) {
	keys := templateAllKeys(t)
	macros, err := TemplateFactoryDefaults()
	if err != nil {
		t.Fatal(err)
	}
	share := func(who, a, b string) {
		t.Helper()
		if keys[a] == nil || keys[b] == nil {
			t.Errorf("%s: template %q or %q isn't shipped", who, a, b)
			return
		}
		for k := range keys[a] {
			if keys[b][k] {
				t.Errorf("%s: key %s is in both %q and %q", who, k, a, b)
			}
		}
		for m := range macros[a] {
			if _, both := macros[b][m]; both {
				t.Errorf("%s: macro %s is defined by both %q and %q", who, m, a, b)
			}
		}
	}
	// with holds a set and the sets it needs, as a host would carry it.
	var with func(id string) []string
	with = func(id string) []string {
		s, _ := SetByID(id)
		out := []string{id}
		for _, n := range s.Needs {
			out = append(out, with(n)...)
		}
		return out
	}
	together := func(a, b Set) bool {
		ids := append(with(a.ID), with(b.ID)...)
		for _, c := range Classes() {
			if CheckSets(ids, c.ID, c.Internal) == nil {
				return true
			}
		}
		return false
	}
	all := Sets()
	for i, a := range all {
		// What Argus links beside a host's sets: Ping, its push sensors, its web scenarios.
		for _, tpl := range []string{TemplateBasePing, TemplatePush, TemplateWeb} {
			share(a.ID, a.Template, tpl)
		}
		for _, b := range all[i+1:] {
			if together(a, b) {
				share(a.ID+" + "+b.ID, a.Template, b.Template)
			}
		}
	}
	for _, c := range Classes() {
		if err := CheckSets(c.Sets, c.ID, c.Internal); err != nil {
			t.Errorf("class %s: its usual sets can't be on one host: %v", c.ID, err)
		}
		if !c.Internal {
			continue
		}
		// An Argus-managed host carries its own health template beside the sets offered on it.
		for _, s := range all {
			if s.OffersOn(c.ID, true) {
				for _, tpl := range c.Templates {
					share("class "+c.ID+" + "+s.ID, tpl, s.Template)
				}
			}
		}
	}
}

// Every set's template is shipped, no two sets link one template, and every non-internal class is
// made of sets alone (its templates, settings and presets come from them).
func TestSetCatalog(t *testing.T) {
	keys := templateAllKeys(t)
	macros, _ := TemplateFactoryDefaults()
	groups := map[string]bool{}
	for _, g := range SetGroups() {
		groups[g.ID] = true
	}
	ids, tpls := map[string]bool{}, map[string]bool{}
	for _, s := range Sets() {
		if ids[s.ID] || tpls[s.Template] {
			t.Errorf("set %s: its id or template %q is used twice", s.ID, s.Template)
		}
		ids[s.ID], tpls[s.Template] = true, true
		if keys[s.Template] == nil {
			t.Errorf("set %s: template %q isn't shipped", s.ID, s.Template)
		}
		if !groups[s.Group] || s.Label == "" || s.Description == "" {
			t.Errorf("set %s: needs a known group, a label and a description", s.ID)
		}
		if s.Slot != "" && SlotNote(s.Slot) == "" {
			t.Errorf("set %s: slot %q has no rule to show", s.ID, s.Slot)
		}
		for _, n := range s.Needs {
			if _, ok := SetByID(n); !ok {
				t.Errorf("set %s needs an unknown set %q", s.ID, n)
			}
		}
		// A setting the template never reads would be a field that does nothing.
		for _, ms := range s.Macros {
			if _, ok := macros[s.Template][ms.Macro]; !ok {
				t.Errorf("set %s: its template doesn't define %s", s.ID, ms.Macro)
			}
		}
	}
	for _, c := range Classes() {
		if c.Internal {
			continue
		}
		if len(c.Templates) != len(c.Sets) {
			t.Errorf("class %s: %d templates for %d sets", c.ID, len(c.Templates), len(c.Sets))
		}
	}
}

// The rules a host's sets follow: one of a slot, a set with what it needs, the probe's own sets on
// the probe host only.
func TestSetRules(t *testing.T) {
	ssh, _ := SetByID("linux-ssh")
	if why := SetBlock(ssh, []string{"adguard", "dns-resolve"}); why != "" {
		t.Errorf("Linux by SSH on an AdGuard host: %q", why)
	}
	if why := SetBlock(ssh, []string{"linux-snmp"}); why != "one system per host: Linux by SNMP is on" {
		t.Errorf("a second system: %q", why)
	}
	unraid, _ := SetByID("unraid")
	if why := SetBlock(unraid, []string{"adguard"}); why != "needs Linux by SNMP" {
		t.Errorf("unRAID alone: %q", why)
	}
	if why := SetBlock(unraid, []string{"linux-snmp"}); why != "" {
		t.Errorf("unRAID on Linux by SNMP: %q", why)
	}
	for _, c := range []struct {
		ids      []string
		class    string
		internal bool
		ok       bool
	}{
		{[]string{"adguard", "dns-resolve", "linux-snmp", "http"}, "adguard", false, true},
		{[]string{"linux-snmp", "linux-ssh"}, "linux-snmp", false, false},
		{[]string{"unifi-switch", "windows-snmp"}, "unifi-switch", false, false},
		{[]string{"ups-nut", "ups-peanut"}, "nut-ups", false, false},
		{[]string{"unraid"}, "unraid", false, false},
		{[]string{"linux-snmp", "unraid", "xcpng"}, "", false, true},
		{[]string{"speedtest"}, "linux-snmp", false, false},
		{[]string{"speedtest", "saas"}, ClassProbe, true, true},
		{[]string{"http"}, ClassProbe, true, false},
		{[]string{"nope"}, "base", false, false},
	} {
		if err := CheckSets(c.ids, c.class, c.internal); (err == nil) != c.ok {
			t.Errorf("%v on %q: ok=%v, got %v", c.ids, c.class, c.ok, err)
		}
	}
}

// Argus reads a sensor by its key alone (its label, its category, whether it is plumbing), so a key
// two templates carry must mean one thing: the switch's wired-client list (text) once shared
// unifi.clients with an access point's client count and showed on every switch as "Connected
// clients" under Wireless. Every item key has one value type across all templates.
func TestItemKeysMeanOneThing(t *testing.T) {
	docs, _, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	type seen struct{ vt, tpl string }
	byKey := map[string]seen{}
	for _, d := range docs {
		section, key, vt := "", "", ""
		flush := func() {
			if key == "" {
				return
			}
			if vt == "" {
				vt = "UNSIGNED" // the export's default
			}
			if s, ok := byKey[key]; ok && s.vt != vt {
				t.Errorf("item %s is %s in %s but %s in %s: give one of them its own key", key, s.vt, s.tpl, vt, d.name)
			} else if !ok {
				byKey[key] = seen{vt, d.name}
			}
			key, vt = "", ""
		}
		for _, line := range strings.Split(strings.ReplaceAll(d.content, "\r\n", "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") && strings.HasSuffix(line, ":"):
				flush()
				section = strings.TrimSuffix(strings.TrimSpace(line), ":")
			case section != "items":
			case strings.HasPrefix(line, "        - uuid: "):
				flush()
			case strings.HasPrefix(line, "          key: "):
				key = strings.Trim(strings.TrimPrefix(line, "          key: "), "'")
			case strings.HasPrefix(line, "          value_type: "):
				vt = strings.TrimPrefix(line, "          value_type: ")
			}
		}
		flush()
	}
}

// A RESOLVED notification is titled with what came back ("GitHub answers again from this site"), not
// the problem that ended: every trigger in an Argus template carries that sentence as its
// argus.recovery tag (the notifier keeps it from the problem's tags while the alert is open).
func TestTriggersSayHowTheyRecover(t *testing.T) {
	docs, _, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " -")) }
	for _, d := range docs {
		lines := strings.Split(strings.ReplaceAll(d.content, "\r\n", "\n"), "\n")
		for n, line := range lines {
			tl := strings.TrimLeft(line, " -")
			if !strings.HasPrefix(tl, "expression: ") {
				continue
			}
			// The trigger's own fields share the expression's indent; the block ends where one shallower starts.
			ind, name, value := indent(line), "", ""
			for i := n + 1; i < len(lines) && (strings.TrimSpace(lines[i]) == "" || indent(lines[i]) >= ind) && !(indent(lines[i]) == ind && strings.HasPrefix(strings.TrimLeft(lines[i], " "), "- ")); i++ {
				l := strings.TrimSpace(lines[i])
				switch {
				case indent(lines[i]) == ind && strings.HasPrefix(l, "name: "):
					name = strings.TrimPrefix(l, "name: ")
				case strings.HasPrefix(l, "- tag: argus.recovery") && i+1 < len(lines):
					value = strings.Trim(strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "value: "), "'")
				}
			}
			if value == "" {
				t.Errorf("%s line %d: trigger %s has no argus.recovery sentence", d.name, n+1, name)
			}
			if strings.ContainsAny(value, "\u2013\u2014") {
				t.Errorf("%s: trigger %s recovery uses a dash: %q", d.name, name, value)
			}
		}
	}
}
