// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/provision"
)

func TestProbeHostNaming(t *testing.T) {
	if got := probeSite("proxy-site1"); got != "site1" {
		t.Fatalf("probeSite = %q", got)
	}
	if got := probeHostName("proxy-site1"); got != "argus-probe-site1" {
		t.Fatalf("probeHostName = %q", got)
	}
	if got := probeHostName("edge"); got != "argus-probe-edge" { // a proxy outside the proxy-<site> naming
		t.Fatalf("probeHostName(edge) = %q", got)
	}
}

func TestClassifyProbeHealthKeys(t *testing.T) {
	cases := []struct {
		key                           string
		cat, label, instance, channel string
	}{
		{"zabbix[uptime]", "Uptime", "Probe uptime", "", ""},
		{"zabbix[proxy_history]", "Probe", "Unsent values", "", ""},
		{"zabbix[queue,10m]", "Probe", "Delayed items (over 10 min)", "", ""},
		{"zabbix[wcache,history,pused]", "Probe", "History cache used", "", ""},
		{"zabbix[rcache,buffer,pused]", "Probe", "Configuration cache used", "", ""},
		{"zabbix[process,poller,avg,busy]", "Probe", "Process load (poller)", "Process load", "Poller"},
		{`zabbix[process,"http agent poller",avg,busy]`, "Probe", "Process load (http agent poller)", "Process load", "Http agent poller"},
	}
	for _, c := range cases {
		cat, label, inst, ch, ok := classifyItem(c.key, "")
		if !ok || cat != c.cat || label != c.label || inst != c.instance || ch != c.channel {
			t.Errorf("%s -> (%q, %q, %q, %q, %v), want (%q, %q, %q, %q)", c.key, cat, label, inst, ch, ok, c.cat, c.label, c.instance, c.channel)
		}
	}
	if _, _, _, _, ok := classifyItem("zabbix[vmware,buffer,pused]", ""); ok {
		t.Error("an unlisted internal key must stay uncurated")
	}
}

func TestProbeClassHasNoPing(t *testing.T) {
	c, ok := provision.ClassByID(provision.ClassProbe)
	if !ok || !c.Internal {
		t.Fatalf("probe class missing or not internal: %+v", c)
	}
	for _, tpl := range c.HostTemplates() {
		if tpl == provision.TemplateBasePing {
			t.Fatal("the Probe host has no address, so no Base Ping")
		}
	}
	if lc, _ := provision.ClassByID("linux-snmp"); len(lc.HostTemplates()) == 0 || lc.HostTemplates()[0] != provision.TemplateBasePing {
		t.Fatal("ordinary classes must still lead with Base Ping")
	}
}
