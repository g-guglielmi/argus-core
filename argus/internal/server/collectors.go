// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"fmt"
	"strings"
)

// The probe's external collectors. Each template's master item runs one for all of a host's sensors
// of its kind (every URL, every port), and its sensors are only discovered from its answer. So while
// a collector fails, its sensors go quiet or never appear, and the master item itself is not a curated
// sensor: nothing showed it. A collector new in an Argus release fails until the probe has it (or, for
// a host the core monitors, the core's copy). A failing collector is therefore a sensor of its own, in
// its sensors' category, saying why, and it alerts even when it never collected (the templates are
// written to answer with their own errors, so "not supported" always means it can't run).

type collector struct {
	label    string // what the row is called
	category string // the sensors it feeds
	since    string // the probe release that ships it
}

var collectors = map[string]collector{
	"argus_http.py":      {"HTTP checks", "Web", "probe/v7.0.31-r15"},
	"argus_tcp.py":       {"TCP port checks", "TCP", "probe/v7.0.31-r14"},
	"argus_linux_ssh.py": {"SSH collector", "Status", ""},
	"argus_nut.py":       {"NUT collector", "Power", ""},
	"argus_xcpng.py":     {"XAPI collector", "Status", ""},
	"dns-resolver.py":    {"DNS checks", "DNS", ""},
}

// collectorOf reports whether key is an external collector's master item.
func collectorOf(key string) (collector, bool) {
	c, ok := collectors[keyBase(key)]
	return c, ok
}

// collectorWhy words why a collector isn't running: a script the probe doesn't have says which
// release brings it; anything else is Zabbix's own error.
func collectorWhy(key, zbxErr string) string {
	c, _ := collectorOf(key)
	script := keyBase(key)
	low := strings.ToLower(zbxErr)
	if strings.Contains(zbxErr, script) && (strings.Contains(low, "no such file") || strings.Contains(low, "not found")) {
		if c.since != "" {
			return fmt.Sprintf("%s isn't where this host is monitored yet: a probe has it from %s on; the core gets it from the argus-updater after an update (the Updates page says whether it did)", script, c.since)
		}
		return script + " isn't installed where this host is monitored (on the core, the Updates page says whether the argus-updater installed it)"
	}
	return itemError(zbxErr)
}

// sensorWhy is a census row's reason: a collector's own wording while it can't run, else the usual.
func sensorWhy(ri reasonIndex, hostID, key, value, zbxErr string, supported bool) string {
	if _, ok := collectorOf(key); ok && !supported {
		return collectorWhy(key, zbxErr)
	}
	return ri.why(hostID, key, value, zbxErr, supported)
}
