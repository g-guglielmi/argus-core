// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"
	"time"

	"argus/internal/zabbix"
)

// A discovered SNMP sensor whose index stopped answering is a renumbered instance; anything else isn't.
func TestRenumberedInstance(t *testing.T) {
	cases := []struct {
		it   zabbix.UnsupportedItem
		want bool
	}{
		{zabbix.UnsupportedItem{Type: "20", Flags: "4", Error: "No Such Instance currently exists at this OID"}, true},
		{zabbix.UnsupportedItem{Type: "20", Flags: "4", Error: "No Such Object available on this agent at this OID"}, true},
		{zabbix.UnsupportedItem{Type: "20", Flags: "0", Error: "No Such Instance currently exists at this OID"}, false}, // a plain item: no discovery to repoint it
		{zabbix.UnsupportedItem{Type: "20", Flags: "4", Error: "Timeout while connecting to \"10.0.0.20:161\"."}, false},
		{zabbix.UnsupportedItem{Type: "0", Flags: "4", Error: "No Such Instance"}, false}, // not SNMP
	}
	for i, c := range cases {
		if got := renumberedInstance(c.it); got != c.want {
			t.Errorf("case %d: got %v, want %v", i, got, c.want)
		}
	}
}

// A UniFi port or radio its discovery no longer finds is gone with the old device, not broken; a
// drive or VM that drops out, or a port still found, is news.
func TestGoneFromDevice(t *testing.T) {
	lost := zabbix.ItemDiscovery{Status: "1"}
	cases := []struct {
		it   zabbix.UnsupportedItem
		want bool
	}{
		{zabbix.UnsupportedItem{Flags: "4", Key: "unifi.port.in[3]", Discovery: lost}, true},
		{zabbix.UnsupportedItem{Flags: "4", Key: "unifi.port.poe[2]", Discovery: lost}, true},
		{zabbix.UnsupportedItem{Flags: "4", Key: `unifi.radio.util[ng,"2.4 GHz"]`, Discovery: lost}, true},
		{zabbix.UnsupportedItem{Flags: "4", Key: "unifi.port.in[3]"}, false},                         // still found: it failed
		{zabbix.UnsupportedItem{Flags: "4", Key: "unraid.disk.temp[disk3]", Discovery: lost}, false}, // a drive dropping out is news
		{zabbix.UnsupportedItem{Flags: "4", Key: "xcp.vm.cpu[web1]", Discovery: lost}, false},        // so is a VM
		{zabbix.UnsupportedItem{Flags: "0", Key: "unifi.poe.total", Discovery: lost}, false},         // not discovered
	}
	for i, c := range cases {
		if got := goneFromDevice(c.it); got != c.want {
			t.Errorf("case %d: got %v, want %v", i, got, c.want)
		}
	}
}

// A host's discovery runs once per rediscoverEvery, not on every notifier tick.
func TestRediscoverDue(t *testing.T) {
	now := time.Now()
	if !rediscoverDue("t-host", now) {
		t.Fatal("first request: due")
	}
	if rediscoverDue("t-host", now.Add(time.Minute)) {
		t.Fatal("a minute later: not again")
	}
	if !rediscoverDue("t-host", now.Add(rediscoverEvery+time.Second)) {
		t.Fatal("after rediscoverEvery: due again")
	}
	if !rediscoverDue("t-other", now) {
		t.Fatal("another host: its own clock")
	}
}
