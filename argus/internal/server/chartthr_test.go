// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/zabbix"
)

func trig(prio int, items int, expr string) zabbix.ItemTrigger {
	return zabbix.ItemTrigger{Priority: prio, ItemCount: items, Expression: expr}
}

func TestItemThresholdsFrom(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name  string
		key   string
		trigs []zabbix.ItemTrigger
		want  *itemThresholds
	}{
		{
			name: "warn band + high (macro-resolved, as trigger.get expandExpression returns it)",
			key:  "unraid.cputemp",
			trigs: []zabbix.ItemTrigger{
				trig(2, 1, `min(/nas1/unraid.cputemp,#3)>=85 and min(/nas1/unraid.cputemp,#3)<95`),
				trig(4, 1, `min(/nas1/unraid.cputemp,#3)>=95`),
			},
			want: &itemThresholds{Warn: f(85), High: f(95)},
		},
		{
			name: "fractional values and a quoted LLD key",
			key:  `unraid.disktemp["disk1"]`,
			trigs: []zabbix.ItemTrigger{
				trig(2, 1, `min(/nas1/unraid.disktemp["disk1"],#3)>=40 and min(/nas1/unraid.disktemp["disk1"],#3)<45`),
				trig(4, 1, `min(/nas1/unraid.disktemp["disk1"],#3)>=45`),
			},
			want: &itemThresholds{Warn: f(40), High: f(45)},
		},
		{
			name: "multi-item trigger only counts comparisons on this item",
			key:  "dns.resolve.time[example.lan]",
			trigs: []zabbix.ItemTrigger{
				trig(2, 2, `min(/dns1/dns.resolve.time[example.lan],#3)>=0.5 and min(/dns1/dns.resolve.time[example.lan],#3)<1 and max(/dns1/dns.resolve.success[example.lan],#3)=1`),
				trig(4, 2, `min(/dns1/dns.resolve.time[example.lan],#3)>=1 and max(/dns1/dns.resolve.success[example.lan],#3)=1`),
			},
			want: &itemThresholds{Warn: f(0.5), High: f(1)},
		},
		{
			name: "key params with empty fields",
			key:  "net.tcp.service.perf[https,,443]",
			trigs: []zabbix.ItemTrigger{
				trig(4, 1, `min(/web1/net.tcp.service.perf[https,,443],#3)>=3`),
			},
			want: &itemThresholds{High: f(3)},
		},
		{
			name: "lower is worse (battery), with a band trigger",
			key:  "ups.battery",
			trigs: []zabbix.ItemTrigger{
				trig(2, 1, `last(/ups1/ups.battery)<30 and last(/ups1/ups.battery)>=10`),
				trig(4, 1, `last(/ups1/ups.battery)<10`),
			},
			want: &itemThresholds{Warn: f(30), High: f(10), Below: true},
		},
		{
			name:  "unit suffix",
			key:   "vfs.fs.size[/,free]",
			trigs: []zabbix.ItemTrigger{trig(3, 1, `last(/srv1/vfs.fs.size[/,free])<10G`)},
			want:  &itemThresholds{High: f(10 * 1024 * 1024 * 1024), Below: true},
		},
		{
			name:  "up/down equality is not a threshold",
			key:   "icmpping",
			trigs: []zabbix.ItemTrigger{trig(4, 1, `max(/h1/icmpping,#3)=0`)},
		},
		{
			name:  "edge detector (same value both ways) is skipped",
			key:   "unifi.wan.avail[1]",
			trigs: []zabbix.ItemTrigger{trig(2, 1, `max(/gw1/unifi.wan.avail[1],3h)>=99 and max(/gw1/unifi.wan.avail[1],#3)<99`)},
		},
		{
			name:  "arithmetic left side is skipped",
			key:   "a",
			trigs: []zabbix.ItemTrigger{trig(4, 2, `last(/h1/a)/last(/h1/b)*100>90`)},
		},
		{
			name:  "item-to-item comparison is skipped",
			key:   "xcp.hosts.live",
			trigs: []zabbix.ItemTrigger{trig(3, 2, `last(/pool1/xcp.hosts.live)<last(/pool1/xcp.hosts.total) and max(/pool1/xcp.reachable,#2)=1`)},
		},
		{
			name:  "unresolved macro is ignored",
			key:   "system.cpu.util",
			trigs: []zabbix.ItemTrigger{trig(2, 1, `min(/h1/system.cpu.util,#3)>={$CPU.UTIL.WARN}`)},
		},
		{
			name:  "info severity does not band",
			key:   "system.cpu.util",
			trigs: []zabbix.ItemTrigger{trig(1, 1, `min(/h1/system.cpu.util,#3)>=50`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := itemThresholdsFrom(tc.trigs, tc.key)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got == nil {
				return
			}
			eq := func(a, b *float64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
			if !eq(got.Warn, tc.want.Warn) || !eq(got.High, tc.want.High) || got.Below != tc.want.Below {
				t.Fatalf("got warn=%v high=%v below=%v, want warn=%v high=%v below=%v",
					ptrStr(got.Warn), ptrStr(got.High), got.Below, ptrStr(tc.want.Warn), ptrStr(tc.want.High), tc.want.Below)
			}
		})
	}
}

func ptrStr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
