// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReasonKeyFor(t *testing.T) {
	for key, want := range map[string]string{
		"linux.ssh.reachable":                  "linux.ssh.error",
		"xcp.authed":                           "xcp.error",
		"dns.resolve.success[nas.example.lan]": "dns.resolve.error[nas.example.lan]",
		"icmpping":                             "",
		"linux.ssh.error":                      "",
	} {
		if got := reasonKeyFor(key); got != want {
			t.Errorf("reasonKeyFor(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestItemError(t *testing.T) {
	for in, want := range map[string]string{
		"Cannot execute script: UniFi API HTTP 400 (api.err.NoSiteContext)": "UniFi API HTTP 400 (api.err.NoSiteContext)",
		"Cannot execute script: Error: cannot get URL: timed out":           "cannot get URL: timed out",
		"Timeout while executing a shell script.\nmore detail":              "Timeout while executing a shell script.",
		"  ": "",
	} {
		if got := itemError(in); got != want {
			t.Errorf("itemError(%q) = %q, want %q", in, got, want)
		}
	}
	if got := itemError(strings.Repeat("x", 300)); len([]rune(got)) != 201 {
		t.Errorf("long error not capped: %d runes", len([]rune(got)))
	}
}

// A sensor shows Zabbix's error while it's not supported, and a collector flag that reads down shows
// its collector's reason; anything else shows nothing.
func TestReasonIndexWhy(t *testing.T) {
	ri := reasonIndex{}
	ri.add("10", "linux.ssh.error", "root@nas: Permission denied (publickey).")
	ri.add("10", "dns.resolve.error[a.example.lan]", "the server answered NXDOMAIN")
	ri.add("10", "system.cpu.util[ssh]", "not a reason item")
	ri.add("11", "linux.ssh.error", "")

	cases := []struct {
		host, key, value, zerr string
		supported              bool
		want                   string
	}{
		{"10", "linux.ssh.reachable", "0", "", true, "root@nas: Permission denied (publickey)."},
		{"10", "linux.ssh.reachable", "1", "", true, ""},
		{"11", "linux.ssh.reachable", "0", "", true, ""},
		{"10", "dns.resolve.success[a.example.lan]", "0", "", true, "the server answered NXDOMAIN"},
		{"10", "dns.resolve.success[b.example.lan]", "0", "", true, ""},
		{"10", "unifi.cpu.util", "", "Cannot execute script: UniFi API HTTP 401 (Unauthorized)", false, "UniFi API HTTP 401 (Unauthorized)"},
		{"10", "system.cpu.util[ssh]", "12", "", true, ""},
	}
	for _, c := range cases {
		if got := ri.why(c.host, c.key, c.value, c.zerr, c.supported); got != c.want {
			t.Errorf("why(%s, %s=%s) = %q, want %q", c.host, c.key, c.value, got, c.want)
		}
	}
}

func TestWithReason(t *testing.T) {
	if got := withReason("Not reachable", "Connection refused"); got != "Not reachable: Connection refused" {
		t.Errorf("got %q", got)
	}
	if got := withReason("0", "XAPI rejected the user name or password"); got != "XAPI rejected the user name or password" {
		t.Errorf("got %q", got)
	}
	if got := withReason("Not reachable", ""); got != "Not reachable" {
		t.Errorf("got %q", got)
	}
}

// Every collector flag Argus knows has a reason item, and the templates carry both: a new collector
// template that reports "down" without saying why fails here (DESIGN section 5).
func TestTemplatesSayWhy(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "provision", "templates", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates: %v", err)
	}
	all := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all += string(raw)
	}
	for _, k := range collectorMasterKeys {
		if reasonKeyFor(k) == "" {
			t.Errorf("collector flag %s has no reason item in reasonKeys", k)
		}
	}
	for flag, reason := range reasonKeys {
		if !strings.Contains(all, "key: "+flag+"\n") && !strings.Contains(all, "key: '"+flag+"[") {
			t.Errorf("no template item %s", flag)
		}
		if !strings.Contains(all, "key: "+reason+"\n") && !strings.Contains(all, "key: '"+reason+"[") {
			t.Errorf("no template item %s (the reason for %s)", reason, flag)
		}
	}
	// A script template's refusal must carry more than the status code.
	if strings.Contains(all, "throw 'UniFi API HTTP ' + req.getStatus()") {
		t.Error("a UniFi template still throws the bare HTTP status")
	}
}

// A systemd unit or a Docker container reads Running / Down, rows under Services / Containers named
// after it, has an uptime, and its state text is its reason.
func TestServiceContainerSensors(t *testing.T) {
	for key, want := range map[string]string{"linux.ssh.unit.active[nginx]": "Running", "linux.ssh.container.running[jellyfin]": "Running"} {
		if got, ok := reachabilityReading(key, "1"); !ok || got != want {
			t.Errorf("%s = 1 reads %q", key, got)
		}
		if got, _ := reachabilityReading(key, "0"); got != "Down" {
			t.Errorf("%s = 0 reads %q", key, got)
		}
		if !isUpDownKey(key) {
			t.Errorf("%s has no uptime", key)
		}
	}
	if cat, label, _, _, ok := classifyItem("linux.ssh.unit.active[nginx]", "Service nginx"); !ok || cat != "Services" || label != "nginx" {
		t.Errorf("unit row = %q %q %v", cat, label, ok)
	}
	if cat, label, _, _, ok := classifyItem("linux.ssh.container.running[jellyfin]", "Container jellyfin"); !ok || cat != "Containers" || label != "jellyfin" {
		t.Errorf("container row = %q %q %v", cat, label, ok)
	}
	if _, _, _, _, ok := classifyItem("linux.ssh.unit.state[nginx]", "Service nginx state"); ok {
		t.Error("a unit's state item must not be a sensor row")
	}
	if got := reasonKeyFor("linux.ssh.container.running[jellyfin]"); got != "linux.ssh.container.status[jellyfin]" {
		t.Errorf("container reason key = %q", got)
	}
	if _, ok := categoryOrderServer["Containers"]; !ok {
		t.Error("Containers has no place in the category order")
	}
}

// A host's tree dot pulses only for a warning or error nobody has acknowledged.
func TestUnackedHosts(t *testing.T) {
	got := unackedHosts([]sensorRow{
		{HostID: "1", State: "error"}, {HostID: "2", State: "acked"}, {HostID: "3", State: "warning"},
		{HostID: "4", State: "ok"}, {HostID: "5", State: "paused"}, {HostID: "2", State: "hidden"},
	})
	if !got["1"] || !got["3"] || got["2"] || got["4"] || got["5"] {
		t.Fatalf("unacked hosts = %v, want only 1 and 3", got)
	}
}

// A host reads as acknowledged only when everything wrong on it is: an acked sensor and no open one.
func TestAckedHosts(t *testing.T) {
	got := ackedHosts([]sensorRow{
		{HostID: "1", State: "acked"}, {HostID: "1", State: "ok"},
		{HostID: "2", State: "acked"}, {HostID: "2", State: "warning"},
		{HostID: "3", State: "error"}, {HostID: "4", State: "ok"}, {HostID: "5", State: "held"},
	})
	if !got["1"] || got["2"] || got["3"] || got["4"] || got["5"] {
		t.Fatalf("acked hosts = %v, want only 1", got)
	}
}

// The failed-units count shows the failed names while it is above 0, has no uptime, and is a Services
// row; the CPU iowait and steal sensors are CPU rows.
func TestFailedUnitsSensor(t *testing.T) {
	key := "linux.ssh.units.failed[systemd]"
	if got := reasonKeyFor(key); got != "linux.ssh.units.failed.names[systemd]" {
		t.Fatalf("reason key = %q", got)
	}
	ri := reasonIndex{}
	ri.add("10", "linux.ssh.units.failed.names[systemd]", "backup.service, certbot.timer")
	if got := ri.why("10", key, "2", "", true); got != "backup.service, certbot.timer" {
		t.Errorf("2 failed units: why = %q", got)
	}
	if got := ri.why("10", key, "0", "", true); got != "" {
		t.Errorf("no failed units: why = %q", got)
	}
	if isUpDownKey(key) {
		t.Error("a count has no uptime")
	}
	if cat, label, _, _, ok := classifyItem(key, "Failed units"); !ok || cat != "Services" || label != "Failed units" {
		t.Errorf("failed units row = %q %q %v", cat, label, ok)
	}
	if _, _, _, _, ok := classifyItem("linux.ssh.units.failed.names[systemd]", "Failed unit names"); ok {
		t.Error("the names item must not be a sensor row")
	}
	for k, want := range map[string]string{"system.cpu.util[ssh,iowait]": "CPU utilization (iowait)", "system.cpu.util[ssh,steal]": "CPU utilization (steal)"} {
		if cat, label, _, _, ok := classifyItem(k, ""); !ok || cat != "CPU" || label != want {
			t.Errorf("%s row = %q %q %v", k, cat, label, ok)
		}
	}
}

// A TCP port is one group named after it (connect time primary, reachability the Downtime band), its
// error item is the reason while it reads down, and it has an uptime.
func TestTCPPortSensors(t *testing.T) {
	if cat, label, inst, ch, ok := classifyItem("tcp.port.up[3389]", "Port RDP (3389)"); !ok || cat != "TCP" || inst != "RDP (3389)" || ch != "Reachable" || label != "Port RDP (3389) reachable" {
		t.Errorf("up row = %q %q %q %q %v", cat, label, inst, ch, ok)
	}
	if cat, _, inst, ch, ok := classifyItem("tcp.port.time[3389]", "Port RDP (3389) connect time"); !ok || cat != "TCP" || inst != "RDP (3389)" || ch != "Response time" {
		t.Errorf("time row = %q %q %q %v", cat, inst, ch, ok)
	}
	if _, _, inst, _, _ := classifyItem("tcp.port.up[8443]", "Port {#PORTNAME}"); inst != "8443" {
		t.Errorf("unexpanded name falls back to the port, got %q", inst)
	}
	if _, _, _, _, ok := classifyItem("tcp.port.error[3389]", "Port RDP (3389) error"); ok {
		t.Error("a port's error item must not be a sensor row")
	}
	if got := reasonKeyFor("tcp.port.up[3389]"); got != "tcp.port.error[3389]" {
		t.Errorf("reason key = %q", got)
	}
	if !isUpDownKey("tcp.port.up[3389]") {
		t.Error("a TCP port has an uptime")
	}
	for _, order := range []map[string]int{categoryOrderServer, categoryOrderNet, categoryOrderNAS} {
		if order["TCP"] != order["Web"]+1 {
			t.Errorf("TCP should follow Web: %v", order)
		}
	}
}
