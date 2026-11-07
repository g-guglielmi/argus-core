// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"argus/internal/store"
)

// A sensor names its service, container or Windows service through its key; anything else names none.
func TestSensorTarget(t *testing.T) {
	cases := []struct{ key, kind, name string }{
		{"linux.ssh.unit.active[nginx.service]", "unit", "nginx.service"},
		{"linux.ssh.unit.state[getty@tty1.service]", "unit", "getty@tty1.service"},
		{"linux.ssh.container.running[jellyfin]", "container", "jellyfin"},
		{"linux.ssh.container.status[web-1]", "container", "web-1"},
		{`win.service.state["Print Spooler"]`, "winsvc", "Print Spooler"},
		{"win.service.state[DNS Client]", "winsvc", "DNS Client"},
		{"icmpping", "", ""},
		{"vfs.fs.size[/,pused]", "", ""},
	}
	for _, c := range cases {
		k, n := sensorTarget(c.key)
		if k != c.kind || n != c.name {
			t.Errorf("%s: got %q %q, want %q %q", c.key, k, n, c.kind, c.name)
		}
	}
	if actionKind("linux.ssh.unit.state[x]") != actionKind("linux.ssh.unit.active[y]") {
		t.Error("a unit's two sensors should be one kind")
	}
	if actionKind("linux.ssh.container.status[x]") != "linux.ssh.container.running[*]" {
		t.Error("a container's state sensor should count as its running sensor")
	}
}

// What reaches the shell on the probe: argus_action.py and arguments that are each base64 or a
// btoa() macro in single quotes, never a value as written. An agent command is the admin's own text
// with the sensor's name in single quotes, which the validator keeps free of quotes.
func TestActionScriptsQuoteEverything(t *testing.T) {
	arg := regexp.MustCompile(`^'(?:[A-Za-z0-9+/=]*|\{\{[A-Z.$]+\}\.btoa\(\)\}|\{\{\$[A-Z0-9.]+\}\.btoa\(\)\}|\{MANUALINPUT\})'$`)
	cfgs := []struct {
		a store.Action
		c actionConfig
	}{
		{store.Action{ID: 1, Name: "Restart", Type: "service", Via: "ssh"}, actionConfig{OS: "linux"}},
		{store.Action{ID: 2, Name: "Restart c", Type: "container", Via: "ssh"}, actionConfig{}},
		{store.Action{ID: 3, Name: "Clean; rm -rf / 'x'", Type: "command", Via: "ssh"}, actionConfig{Command: "sudo -n journalctl --vacuum-size=500M; echo '$(id)'"}},
		{store.Action{ID: 4, Name: "Save", Type: "netscaler", Via: "probe"}, actionConfig{}},
		{store.Action{ID: 5, Name: "Hook", Type: "webhook", Via: "probe"}, actionConfig{URL: "https://hooks.example.com/x?a=1&b=$(id)", Method: "POST", Header: "X-Token", HasSecret: true}},
		{store.Action{ID: 6, Name: "Hook2", Type: "webhook", Via: "probe"}, actionConfig{URL: "https://hooks.example.com/", Method: "GET"}},
	}
	for _, x := range cfgs {
		sc := actionScript(x.a, x.c)
		if sc.ExecuteOn != 2 {
			t.Errorf("%s: should run on the probe or the core", x.a.Name)
		}
		if strings.Contains(sc.Name, "/") {
			t.Errorf("%s: a script name with a slash", sc.Name)
		}
		fields := strings.Fields(sc.Command)
		if fields[0] != actionScriptPath {
			t.Fatalf("%s: runs %q", x.a.Name, fields[0])
		}
		for _, f := range fields[2:] {
			if !arg.MatchString(f) {
				t.Errorf("%s: argument %q is not quoted base64 or a btoa() macro", x.a.Name, f)
			}
		}
	}
	// The sensor-bound scripts take the name as Zabbix's checked manual input.
	if sc := actionScript(cfgs[0].a, cfgs[0].c); sc.Validator != unitValidator || !regexp.MustCompile(sc.Validator).MatchString(sc.InputDefault) {
		t.Errorf("service script input: %+v", sc)
	}
	if sc := actionScript(cfgs[4].a, cfgs[4].c); !strings.Contains(sc.Command, "{$ARGUS.ACTION.5.SECRET}") {
		t.Errorf("a webhook with a secret should name its macro: %s", sc.Command)
	}
	if sc := actionScript(cfgs[5].a, cfgs[5].c); strings.Contains(sc.Command, "SECRET") {
		t.Errorf("a webhook without a secret names none: %s", sc.Command)
	}
	// Agent scripts run on the agent with the name inside single quotes.
	win := actionScript(store.Action{ID: 7, Name: "W", Type: "service", Via: "agent"}, actionConfig{OS: "windows"})
	lin := actionScript(store.Action{ID: 8, Name: "L", Type: "service", Via: "agent"}, actionConfig{OS: "linux"})
	for _, sc := range []struct {
		cmd, val string
		on       int
	}{{win.Command, win.Validator, win.ExecuteOn}, {lin.Command, lin.Validator, lin.ExecuteOn}} {
		if sc.on != 0 || !strings.Contains(sc.cmd, "'{MANUALINPUT}'") || sc.val == "" {
			t.Errorf("agent script: %+v", sc)
		}
	}
}

// The validators keep anything a shell or PowerShell reads out of a name.
func TestActionValidators(t *testing.T) {
	bad := []string{"a'b", "a;b", "a b", "$(id)", "`id`", "-rf", "a\nb", "a|b", "a&b", ""}
	for _, v := range bad {
		if unitRe.MatchString(v) || containerRe.MatchString(v) {
			t.Errorf("unit/container validator let %q through", v)
		}
	}
	for _, v := range []string{"a'b", "a;b", "$(id)", "a\"b", "a,b", "a&b", "-x"} {
		if winsvcRe.MatchString(v) {
			t.Errorf("windows validator let %q through", v)
		}
	}
	for _, v := range []string{"nginx.service", "getty@tty1.service", "dev-sda1.mount"} {
		if !unitRe.MatchString(v) {
			t.Errorf("unit validator refused %q", v)
		}
	}
	if !winsvcRe.MatchString("Print Spooler") || !winsvcRe.MatchString("SQL Server (MSSQLSERVER)") {
		t.Error("windows validator refused a real display name")
	}
}

func TestCheckAction(t *testing.T) {
	ok := []actionReq{
		{Name: "Restart", Type: "service", Via: "ssh"},
		{Name: "Restart W", Type: "service", Via: "agent"},
		{Name: "Clean", Type: "command", Via: "ssh", Config: actionConfig{Command: "sudo -n journalctl --vacuum-size=500M"}},
		{Name: "Hook", Type: "webhook", Via: "probe", Config: actionConfig{URL: "https://hooks.example.com/x", Body: `{"host":"{host}","n":1}`, Header: "X-Token"}},
	}
	for _, r := range ok {
		if _, _, why := checkAction(r); why != "" {
			t.Errorf("%s refused: %s", r.Name, why)
		}
	}
	bad := []actionReq{
		{Name: "", Type: "service", Via: "ssh"},
		{Name: "x", Type: "container", Via: "agent"},
		{Name: "x", Type: "netscaler", Via: "ssh"},
		{Name: "x", Type: "nope", Via: "ssh"},
		{Name: "x", Type: "command", Via: "ssh", Config: actionConfig{Command: "a\nb"}},
		{Name: "x", Type: "command", Via: "agent", Config: actionConfig{Command: "echo {$SSH.PASSWORD}"}},
		{Name: "x", Type: "webhook", Via: "probe", Config: actionConfig{URL: "ftp://x"}},
		{Name: "x", Type: "webhook", Via: "probe", Config: actionConfig{URL: "https://u:p@x.example.com/"}},
		{Name: "x", Type: "webhook", Via: "probe", Config: actionConfig{URL: "https://x.example.com/", Body: `{"a": {host}}`}},
		{Name: "x", Type: "webhook", Via: "probe", Config: actionConfig{URL: "https://x.example.com/", Header: "X Token"}},
		{Name: "x", Type: "service", Via: "ssh", CooldownMin: 2000},
	}
	for _, r := range bad {
		if _, _, why := checkAction(r); why == "" {
			t.Errorf("%+v accepted", r)
		}
	}
}

// A webhook's values go in JSON-escaped, so a sensor name can't break out of its string.
func TestFillPlaceholders(t *testing.T) {
	body := fillPlaceholders(`{"text":"{host} {sensor}","who":"{user}"}`, map[string]string{"host": `web"01`, "sensor": "a\\b\n", "user": "admin@example.com"})
	var v map[string]string
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not JSON: %s (%v)", body, err)
	}
	if v["text"] != "web\"01 a\\b\n" || v["who"] != "admin@example.com" {
		t.Errorf("filled: %+v", v)
	}
	in, _, why := actionInput(store.Action{Type: "webhook"}, actionConfig{Body: `{"h":"{host}"}`}, actionTarget{hostName: "web01"}, "")
	if why != "" || !regexp.MustCompile(bodyValidator).MatchString(in) {
		t.Fatalf("webhook input %q %s", in, why)
	}
	raw, _ := base64.StdEncoding.DecodeString(in)
	if string(raw) != `{"h":"web01"}` {
		t.Errorf("decoded %s", raw)
	}
}

func TestActionInputRefusesOddNames(t *testing.T) {
	_, _, why := actionInput(store.Action{Type: "service"}, actionConfig{}, actionTarget{key: "linux.ssh.unit.active[a;b]"}, "")
	if why == "" {
		t.Error("a unit name with ; should not pass")
	}
	in, target, why := actionInput(store.Action{Type: "container"}, actionConfig{}, actionTarget{key: "linux.ssh.container.running[jellyfin]"}, "")
	if why != "" || in != "jellyfin" || target != "jellyfin" {
		t.Errorf("container input %q %q %s", in, target, why)
	}
	if _, _, why := actionInput(store.Action{Type: "service"}, actionConfig{}, actionTarget{key: "icmpping"}, ""); why == "" {
		t.Error("ping names no service")
	}
}

func TestActionZabbixWhy(t *testing.T) {
	tg := actionTarget{hostName: "web01", source: "proxy-site1"}
	cases := map[string]string{
		"Remote commands are not enabled":                                              "proxy-site1 does not run actions yet",
		"Global script execution on Zabbix server is disabled by server configuration": "EnableGlobalScripts",
		"Zabbix agent interface is not defined for host [web01]":                       "no Zabbix agent interface",
		"Unsupported item key.":                                                        "AllowKey",
		"sh: /usr/lib/zabbix/externalscripts/argus_action.py: not found":               "argus_action.py is not on proxy-site1",
	}
	for in, want := range cases {
		if got := actionZabbixWhy(in, tg); !strings.Contains(got, want) {
			t.Errorf("%q -> %q, want %q in it", in, got, want)
		}
	}
}

func TestAgentOutcome(t *testing.T) {
	svc := store.Action{Type: "service", Via: "agent"}
	if ok, _ := agentOutcome(svc, actionConfig{OS: "windows"}, "\r\nRunning\r\n"); !ok {
		t.Error("windows Running should be ok")
	}
	if ok, why := agentOutcome(svc, actionConfig{OS: "windows"}, "Stopped"); ok || why == "" {
		t.Error("windows Stopped should fail")
	}
	if ok, _ := agentOutcome(svc, actionConfig{OS: "linux"}, "ActiveState=active\nSubState=running"); !ok {
		t.Error("linux active should be ok")
	}
	if ok, why := agentOutcome(svc, actionConfig{OS: "linux"}, "sudo: a password is required"); ok || !strings.Contains(why, "password") {
		t.Errorf("sudo password should fail: %s", why)
	}
}

func TestActionSetupLines(t *testing.T) {
	lines, note := actionSetup(store.Action{Type: "service", Via: "ssh"}, actionConfig{}, "argus", []string{"jellyfin.service", "nginx.service"})
	if len(lines) != 1 || lines[0] != "argus ALL=(root) NOPASSWD: /usr/bin/systemctl restart jellyfin.service, /usr/bin/systemctl restart nginx.service" || note == "" {
		t.Errorf("sudoers: %v", lines)
	}
	lines, _ = actionSetup(store.Action{Type: "command", Via: "ssh"}, actionConfig{Command: "sudo -n /usr/bin/journalctl --vacuum-size=500M"}, "argus", nil)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "NOPASSWD: /usr/bin/journalctl --vacuum-size=500M") {
		t.Errorf("command sudoers: %v", lines)
	}
	lines, _ = actionSetup(store.Action{Type: "service", Via: "agent"}, actionConfig{OS: "windows"}, "", nil)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "AllowKey=system.run[") {
		t.Errorf("windows allowkey: %v", lines)
	}
}
