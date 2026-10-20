// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/provision"
)

func TestCoreHostPlan(t *testing.T) {
	cases := []struct {
		linked      []string
		unlinkStock bool
		link        bool
	}{
		{[]string{"Linux by Zabbix agent", "Zabbix server health"}, true, true}, // a stock core: the stock template makes way
		{[]string{"Linux by Zabbix agent"}, false, true},                        // the stock template already gone: just link
		{nil, false, true},
		{[]string{"Linux by Zabbix agent", "Argus Core Health"}, false, false},             // adopted already
		{[]string{"Zabbix server health", "Argus Core Health"}, false, false},              // adopted; never touch the rest again
		{[]string{"Argus Core Health", "Linux by Zabbix agent", "Zabbix server health"}, false, false},
	}
	for _, c := range cases {
		p := coreHostPlan(c.linked)
		if p.unlinkStock != c.unlinkStock || p.link != c.link {
			t.Errorf("%v -> unlink=%v link=%v, want unlink=%v link=%v", c.linked, p.unlinkStock, p.link, c.unlinkStock, c.link)
		}
	}
}

func TestCoreClassIsInternalAndHasNoPing(t *testing.T) {
	c, ok := provision.ClassByID(provision.ClassCore)
	if !ok || !c.Internal {
		t.Fatalf("core class missing or not internal: %+v", c)
	}
	if c.Icon != "core" {
		t.Errorf("core class icon = %q", c.Icon)
	}
	for _, tpl := range c.HostTemplates() {
		if tpl == provision.TemplateBasePing {
			t.Fatal("Argus does not create the server's host, so it adds no Base Ping to it")
		}
	}
	if len(c.HostTemplates()) != 1 || c.HostTemplates()[0] != provision.TemplateCoreHealth {
		t.Errorf("core class templates = %v", c.HostTemplates())
	}
}

func TestClassifyCoreHealthKeys(t *testing.T) {
	// The core template names its items "Core ...", which files the shared internal keys under Core
	// instead of Probe; the server-only keys are curated too.
	cases := []struct {
		key, name                     string
		cat, label, instance, channel string
	}{
		{"zabbix[requiredperformance]", "Core required performance", "Core", "Required performance (values per second)", "", ""},
		{"zabbix[wcache,values,all]", "Core values processed per second", "Core", "Values processed per second", "", ""},
		{"zabbix[vps,written]", "Core values written per second", "Core", "Values written per second", "", ""},
		{"zabbix[hosts]", "Core hosts monitored", "Core", "Hosts monitored", "", ""},
		{"zabbix[items]", "Core items monitored", "Core", "Items monitored", "", ""},
		{"zabbix[items_unsupported]", "Core unsupported items", "Core", "Unsupported items", "", ""},
		{"zabbix[queue,10m]", "Core delayed items (over 10 min)", "Core", "Delayed items (over 10 min)", "", ""},
		{"zabbix[wcache,history,pused]", "Core history cache used", "Core", "History cache used", "", ""},
		{"zabbix[vcache,buffer,pused]", "Core value cache used", "Core", "Value cache used", "", ""},
		{`zabbix[process,"history syncer",avg,busy]`, "Core process load: history syncer", "Core", "Process load (history syncer)", "Process load", "History syncer"},
		{"argus.core.headroom[items]", "Core headroom: items", "Core", "Headroom: items", "", ""},
		{"argus.core.headroom[hosts]", "Core headroom: hosts", "Core", "Headroom: hosts", "", ""},
		{"argus.core.headroom[load]", "Core headroom: load", "Core", "Headroom: load", "", ""},
		{"argus.core.headroom[disk]", "Core headroom: database disk", "Core", "Headroom: database disk", "", ""},
		// the same keys on a Probe host keep filing under Probe
		{"zabbix[wcache,history,pused]", "History cache used", "Probe", "History cache used", "", ""},
		{"zabbix[uptime]", "Probe uptime", "Uptime", "Probe uptime", "", ""},
	}
	for _, c := range cases {
		cat, label, inst, ch, ok := classifyItem(c.key, c.name)
		if !ok || cat != c.cat || label != c.label || inst != c.instance || ch != c.channel {
			t.Errorf("%s (%q) -> (%q, %q, %q, %q, %v), want (%q, %q, %q, %q)", c.key, c.name, cat, label, inst, ch, ok, c.cat, c.label, c.instance, c.channel)
		}
	}
}
