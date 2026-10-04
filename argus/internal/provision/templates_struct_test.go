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
