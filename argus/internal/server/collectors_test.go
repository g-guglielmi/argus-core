// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
)

// A collector that isn't there yet says which probe release brings it and where the core's comes
// from; other failures keep Zabbix's
// words; the row and the alert are named after the collector, not its raw master item.
func TestCollectorWhy(t *testing.T) {
	key := `argus_http.py[10.0.0.20,"","https","443","","verify","10"]`
	got := collectorWhy(key, "/usr/lib/zabbix/externalscripts/argus_http.py: No such file or directory")
	if !strings.Contains(got, "argus_http.py isn't where this host is monitored yet") || !strings.Contains(got, "probe/v7.0.31-r15") || !strings.Contains(got, "argus-updater") {
		t.Errorf("missing script: %q", got)
	}
	if got := collectorWhy(key, "Timeout while executing a shell script."); got != "Timeout while executing a shell script." {
		t.Errorf("timeout: %q", got)
	}
	if got := collectorWhy("dns-resolver.py[check,a,b,53]", "/usr/lib/zabbix/externalscripts/dns-resolver.py: No such file or directory"); !strings.HasPrefix(got, "dns-resolver.py isn't installed where this host is monitored") {
		t.Errorf("a collector with no release noted: %q", got)
	}
	if sensorLabel(key, "HTTP checks raw data") != "HTTP checks" {
		t.Errorf("label: %q", sensorLabel(key, "HTTP checks raw data"))
	}
	if _, ok := collectorOf("adguard.raw"); ok {
		t.Error("only the probe's external collectors")
	}
	if got := sensorWhy(reasonIndex{}, "10", key, "", "/usr/lib/zabbix/externalscripts/argus_http.py: No such file or directory", false); !strings.Contains(got, "isn't where this host is monitored yet") {
		t.Errorf("census reason: %q", got)
	}
}
