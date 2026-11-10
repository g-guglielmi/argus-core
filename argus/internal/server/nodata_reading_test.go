// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// A trigger that fires because its sensor stopped reporting names it in a nodata(); the lists then
// read "Not reporting" instead of the last value from before it went quiet.
func TestNodataOn(t *testing.T) {
	cases := []struct {
		expr, key string
		want      bool
	}{
		{`nodata(/proxy-site1/zabbix[uptime],10m,"strict")=1`, "zabbix[uptime]", true},
		{`nodata(/h/zabbix[uptime],5m,"strict")=1 and nodata(/h/zabbix[uptime],10m,"strict")=0`, "zabbix[uptime]", true},
		{`nodata(/h/vfs.fs.size[/,pused],30m)=1`, "vfs.fs.size[/,pused]", true},
		{`nodata(/h/vfs.fs.size[/,pused],30m)=1`, "vfs.fs.size[/data,pused]", false},
		{`last(/h/zabbix[uptime])<600`, "zabbix[uptime]", false},
		{`nodata(/h/icmpping,5m)=1 or last(/h/win.service.state[DNS Client])<>1`, "win.service.state[DNS Client]", false},
		{`nodata(/h/win.service.state[DNS Client],10m)=1 or last(/h/win.service.state[DNS Client])<>1`, "win.service.state[DNS Client]", true},
	}
	for _, c := range cases {
		if got := nodataOn(c.expr, c.key); got != c.want {
			t.Errorf("nodataOn(%q, %q) = %v, want %v", c.expr, c.key, got, c.want)
		}
	}
}
