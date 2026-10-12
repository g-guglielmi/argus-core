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

// Zabbix refuses a template import when a host would inherit one item key from two of its templates
// (the unRAID class stacks Linux by SNMP and unRAID by SNMP). No class's templates, with Base Ping and
// the add-ons it is offered, may define the same key twice.
func TestClassTemplatesShareNoItemKey(t *testing.T) {
	keys := templateItemKeys(t)
	for _, c := range Classes() {
		stacks := [][]string{c.HostTemplates()}
		for _, a := range AddOns() {
			if a.OffersOn(c.ID, c.Internal) {
				stacks = append(stacks, append(append([]string{}, c.HostTemplates()...), a.Template))
			}
		}
		for _, stack := range stacks {
			seen := map[string]string{}
			for _, tpl := range stack {
				for _, k := range keys[tpl] {
					if other, dup := seen[k]; dup && other != tpl {
						t.Errorf("class %s: item %s is in both %q and %q", c.ID, k, other, tpl)
					}
					seen[k] = tpl
				}
			}
		}
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
