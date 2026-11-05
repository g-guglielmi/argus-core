// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strconv"
	"testing"
	"time"

	"argus/internal/zabbix"
)

// A kit's hosts are the ones its proxy monitors, never its own Probe host nor another proxy's.
func TestKitHosts(t *testing.T) {
	kit := zabbix.Proxy{ProxyID: "7", Name: "proxy-kit1"}
	got := kitHosts(kit, []zabbix.Host{
		{HostID: "1", Name: "argus-probe-kit1", ProxyID: "7"},
		{HostID: "2", Name: "printer", ProxyID: "7"},
		{HostID: "3", Name: "switch", ProxyID: "7"},
		{HostID: "4", Name: "core-switch", ProxyID: "8"},
		{HostID: "5", Name: "zabbix", ProxyID: "0"},
	})
	if len(got) != 2 || got[0].HostID != "2" || got[1].HostID != "3" {
		t.Fatalf("kit hosts = %+v, want printer and switch", got)
	}
}

// Away means seen once and not for longer than the days; 0 days never.
func TestKitAway(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(ago time.Duration) zabbix.Proxy {
		return zabbix.Proxy{LastAccess: strconv.FormatInt(now.Add(-ago).Unix(), 10)}
	}
	for _, tc := range []struct {
		p    zabbix.Proxy
		days int
		want bool
	}{
		{at(15 * 24 * time.Hour), 14, true},
		{at(13 * 24 * time.Hour), 14, false},
		{at(15 * 24 * time.Hour), 0, false},
		{zabbix.Proxy{LastAccess: "0"}, 14, false},
	} {
		if got := kitAway(tc.p, tc.days, now); got != tc.want {
			t.Errorf("last %s, %d days: away = %v, want %v", tc.p.LastAccess, tc.days, got, tc.want)
		}
	}
}
