// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Actions (DESIGN 7r): fixes someone runs from a sensor in trouble. Admins define them; admins and
// helpdesk run them, a scoped user only on hosts at their sites. Each action is a Zabbix global
// script Argus owns; a run is script.execute on the host, so Zabbix runs it where the host is
// monitored (the site's probe or the core's Zabbix server). SSH, NetScaler and webhook actions run
// argus_action.py there; agent actions run their command through the host's Zabbix agent.
//
// No value reaches a shell as written: the script's arguments are base64 ({{$MACRO}.btoa()}), a
// sensor's service or container comes in as Zabbix's manual input, which Zabbix checks against the
// script's validator before anything runs, and secrets stay in Zabbix macros.

const (
	actionsMax         = 50
	actionNameMax      = 60
	actionCommandMax   = 500
	actionBodyMax      = 1000 // a webhook body after its placeholders are filled in, in bytes
	actionCooldownDef  = 10
	actionCooldownMax  = 24 * 60
	actionRunKeep      = 15 * time.Minute    // a finished run can be read back this long
	actionRunLimit     = 85 * time.Second    // Argus gives up on Zabbix after this
	actionCheckFor     = 45 * time.Second    // how long a run that worked waits for the sensor's new reading
	actionRunsKeep     = 90 * 24 * time.Hour // the run history
	actionScriptPath   = "/usr/lib/zabbix/externalscripts/argus_action.py"
	actionSecretPrefix = "{$ARGUS.ACTION."
)

// The types an action can be, and how each may reach the host.
var actionVias = map[string][]string{
	"service":   {"ssh", "agent"},
	"container": {"ssh"},
	"command":   {"ssh", "agent"},
	"netscaler": {"probe"},
	"webhook":   {"probe"},
}

var actionTypeName = map[string]string{
	"service":   "Restart a service",
	"container": "Restart a container",
	"command":   "Run a command",
	"netscaler": "Save the NetScaler config",
	"webhook":   "Call a webhook",
}

// What a sensor names: a systemd unit, a Docker container or a Windows service (by display name),
// from its item key. The validators are Zabbix's manual input checks (and Argus's own, before a run).
var (
	unitKeyRe      = regexp.MustCompile(`^linux\.ssh\.unit\.(active|state)\[(.+)\]$`)
	containerKeyRe = regexp.MustCompile(`^linux\.ssh\.container\.(running|status)\[(.+)\]$`)
	winsvcKeyRe    = regexp.MustCompile(`^win\.service\.state\[(.+)\]$`)

	unitValidator      = `^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`
	containerValidator = `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`
	winsvcValidator    = `^[A-Za-z0-9][A-Za-z0-9 ._()-]{0,127}$`
	bodyValidator      = `^[A-Za-z0-9+/=]*$` // base64 only: Argus caps its length

	unitRe      = regexp.MustCompile(unitValidator)
	containerRe = regexp.MustCompile(containerValidator)
	winsvcRe    = regexp.MustCompile(winsvcValidator)
	headerRe    = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// sensorTarget is what a sensor's key names for actions: kind "unit", "container" or "winsvc" and
// the name; "" when it names none.
func sensorTarget(key string) (kind, name string) {
	if m := unitKeyRe.FindStringSubmatch(key); m != nil {
		return "unit", unquoteKeyParam(m[2])
	}
	if m := containerKeyRe.FindStringSubmatch(key); m != nil {
		return "container", unquoteKeyParam(m[2])
	}
	if m := winsvcKeyRe.FindStringSubmatch(key); m != nil {
		return "winsvc", unquoteKeyParam(m[1])
	}
	return "", ""
}

// unquoteKeyParam is a key parameter as written: Zabbix quotes one that holds a comma or a bracket.
func unquoteKeyParam(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		return strings.ReplaceAll(p[1:len(p)-1], `\"`, `"`)
	}
	return p
}

// actionKind is the kind an action's "every sensor of this kind" matches: keyKind, with a unit's or
// a container's two sensors (running and its state) counted as one.
func actionKind(key string) string {
	k := keyKind(key)
	switch k {
	case "linux.ssh.unit.state[*]":
		return "linux.ssh.unit.active[*]"
	case "linux.ssh.container.status[*]":
		return "linux.ssh.container.running[*]"
	}
	return k
}

// kindWord is how a kind reads in "every <kind> sensor".
func kindWord(kind, sensor string) string {
	switch kind {
	case "linux.ssh.unit.active[*]":
		return "service"
	case "linux.ssh.container.running[*]":
		return "container"
	case "win.service.state[*]":
		return "Windows service"
	}
	return sensor
}

// actionConfig is an action's own settings (store.Action.Config). Never a secret.
type actionConfig struct {
	OS        string `json:"os,omitempty"`      // agent actions: linux | windows
	Command   string `json:"command,omitempty"` // command
	URL       string `json:"url,omitempty"`     // webhook
	Method    string `json:"method,omitempty"`  // webhook: POST, PUT or GET
	Body      string `json:"body,omitempty"`    // webhook: JSON with placeholders
	Header    string `json:"header,omitempty"`  // webhook: the secret header's name
	HasSecret bool   `json:"has_secret,omitempty"`
}

func readConfig(a store.Action) actionConfig {
	var c actionConfig
	_ = json.Unmarshal([]byte(a.Config), &c)
	return c
}

func secretMacro(id int64) string {
	return actionSecretPrefix + strconv.FormatInt(id, 10) + ".SECRET}"
}

// b64 is a fixed value as a script argument: base64 in single quotes, so no shell reads it as written.
func b64(s string) string { return "'" + base64.StdEncoding.EncodeToString([]byte(s)) + "'" }

// macroArg is a macro's value as a script argument, base64 by Zabbix ({{$SSH.USER}.btoa()}).
func macroArg(macro string) string { return "'{" + macro + ".btoa()}'" }

var sshArgs = strings.Join([]string{macroArg("{HOST.CONN}"), macroArg("{$SSH.USER}"), macroArg("{$SSH.PORT}"),
	macroArg("{$SSH.AUTH}"), macroArg("{$SSH.PASSWORD}"), macroArg("{$SSH.KEYFILE}")}, " ")

// actionScript is the Zabbix script an action runs as.
func actionScript(a store.Action, c actionConfig) zabbix.Script {
	name := strings.ReplaceAll("Argus action "+strconv.FormatInt(a.ID, 10)+": "+a.Name, "/", "-")
	sc := zabbix.Script{ScriptID: a.ScriptID, Name: name, ExecuteOn: 2}
	input := func(prompt, validator, def string) {
		sc.InputPrompt, sc.Validator, sc.InputDefault = prompt, validator, def
	}
	switch {
	case a.Type == "service" && a.Via == "ssh":
		sc.Command = actionScriptPath + " ssh-service " + sshArgs + " " + macroArg("{MANUALINPUT}")
		input("systemd unit", unitValidator, "argus")
	case a.Type == "container":
		sc.Command = actionScriptPath + " ssh-container " + sshArgs + " " + macroArg("{MANUALINPUT}")
		input("container", containerValidator, "argus")
	case a.Type == "command" && a.Via == "ssh":
		sc.Command = actionScriptPath + " ssh-command " + sshArgs + " " + b64(c.Command)
	case a.Type == "service" && a.Via == "agent" && c.OS == "windows":
		sc.ExecuteOn = 0
		sc.Command = `powershell.exe -NoProfile -NonInteractive -Command "Restart-Service -DisplayName '{MANUALINPUT}' -Force; Start-Sleep -Seconds 2; (Get-Service -DisplayName '{MANUALINPUT}').Status"`
		input("Windows service", winsvcValidator, "Argus")
	case a.Type == "service" && a.Via == "agent":
		sc.ExecuteOn = 0
		sc.Command = "sudo -n systemctl restart '{MANUALINPUT}' && sleep 2 && systemctl show --property=ActiveState,SubState '{MANUALINPUT}'"
		input("systemd unit", unitValidator, "argus")
	case a.Type == "command" && a.Via == "agent":
		sc.ExecuteOn = 0
		sc.Command = c.Command
	case a.Type == "netscaler":
		sc.Command = actionScriptPath + " netscaler " + macroArg("{HOST.CONN}") + " " + macroArg("{$NS.USER}") + " " + macroArg("{$NS.PASSWORD}")
	case a.Type == "webhook":
		secret := "''"
		if c.HasSecret && c.Header != "" {
			secret = macroArg(secretMacro(a.ID))
		}
		sc.Command = actionScriptPath + " webhook " + b64(c.URL) + " " + b64(c.Method) + " " + b64(c.Header) + " " + secret + " '{MANUALINPUT}'"
		input("body", bodyValidator, "e30=")
	}
	return sc
}

// actionRemote is what the host will be asked to run, as the Run dialog shows it.
func actionRemote(a store.Action, c actionConfig, target string) string {
	switch {
	case a.Type == "service" && c.OS == "windows":
		return "Restart-Service -DisplayName '" + target + "' -Force"
	case a.Type == "service":
		return "sudo -n systemctl restart " + target
	case a.Type == "container":
		return "docker restart " + target
	case a.Type == "command":
		return c.Command
	case a.Type == "netscaler":
		return "POST /nitro/v1/config/nsconfig?action=save"
	case a.Type == "webhook":
		return c.Method + " " + c.URL
	}
	return ""
}

// ---- the index: which actions a sensor or a host offers ----

type actionIndex struct {
	all     []store.Action
	items   map[string][]int // item id -> positions in all
	kinds   map[string][]int // class|kind
	hosts   map[string][]int
	classes map[string][]int
	hostCls map[string]string // host -> class
	last    map[int64]store.ActionRun
	lastOn  map[string]store.ActionRun
}

var actionCache struct {
	sync.Mutex
	at time.Time
	ix *actionIndex
}

func forgetActions() {
	actionCache.Lock()
	actionCache.at = time.Time{}
	actionCache.Unlock()
}

// loadActions reads the actions, their last runs and the hosts' classes (kept 15 seconds).
func loadActions(ctx context.Context, st *store.Store) *actionIndex {
	actionCache.Lock()
	defer actionCache.Unlock()
	if actionCache.ix != nil && time.Since(actionCache.at) < 15*time.Second {
		return actionCache.ix
	}
	ix := &actionIndex{items: map[string][]int{}, kinds: map[string][]int{}, hosts: map[string][]int{}, classes: map[string][]int{}, hostCls: map[string]string{}}
	if as, err := st.Actions(ctx); err == nil {
		ix.all = as
	}
	for i, a := range ix.all {
		switch a.Scope {
		case "item":
			ix.items[a.ItemID] = append(ix.items[a.ItemID], i)
		case "kind":
			ix.kinds[a.ClassID+"|"+a.KeyKind] = append(ix.kinds[a.ClassID+"|"+a.KeyKind], i)
		case "host":
			ix.hosts[a.HostID] = append(ix.hosts[a.HostID], i)
		case "class":
			ix.classes[a.ClassID] = append(ix.classes[a.ClassID], i)
		}
	}
	if len(ix.kinds)+len(ix.classes) > 0 {
		if cs, err := st.DeviceClasses(ctx); err == nil {
			ix.hostCls = cs
		}
	}
	ix.last, ix.lastOn, _ = st.LastActionRuns(ctx)
	actionCache.at, actionCache.ix = time.Now(), ix
	return ix
}

// forSensor is every action a sensor offers: its own, its kind's, its host's, its host's class's.
// A host's (itemID "") offers its host's and class's.
func (ix *actionIndex) forSensor(hostID, itemID, key string) []store.Action {
	if ix == nil || len(ix.all) == 0 {
		return nil
	}
	var pos []int
	cls := ix.hostCls[hostID]
	if itemID != "" {
		pos = append(pos, ix.items[itemID]...)
		if cls != "" {
			pos = append(pos, ix.kinds[cls+"|"+actionKind(key)]...)
		}
	}
	pos = append(pos, ix.hosts[hostID]...)
	if cls != "" {
		pos = append(pos, ix.classes[cls]...)
	}
	out := make([]store.Action, 0, len(pos))
	seen := map[int]bool{}
	for _, p := range pos {
		if !seen[p] {
			seen[p] = true
			out = append(out, ix.all[p])
		}
	}
	return out
}

// rowAction is an action as a sensor row carries it: enough for the Run button.
type rowAction struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Label   string `json:"label"` // the button's words: "Restart nginx.service", else the name
	LastAt  int64  `json:"last_at,omitempty"`
	LastOK  bool   `json:"last_ok,omitempty"`
	LastWhy string `json:"last_why,omitempty"`
}

func (ix *actionIndex) rowView(hostID, itemID, key string) []rowAction {
	as := ix.forSensor(hostID, itemID, key)
	if len(as) == 0 {
		return nil
	}
	out := make([]rowAction, 0, len(as))
	_, target := sensorTarget(key)
	for _, a := range as {
		v := rowAction{ID: a.ID, Name: a.Name, Label: a.Name}
		if (a.Type == "service" || a.Type == "container") && target != "" {
			v.Label = "Restart " + target
		}
		if r, ok := ix.lastOn[store.ActionHostKey(a.ID, hostID)]; ok {
			v.LastAt, v.LastOK, v.LastWhy = r.At, r.OK, r.Why
		}
		out = append(out, v)
	}
	return out
}

// ---- the admin API ----

// actionView is an action as the Actions page and the editor read it.
type actionView struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	TypeName    string           `json:"type_name"`
	Via         string           `json:"via"`
	Scope       string           `json:"scope"`
	ItemID      string           `json:"item_id,omitempty"`
	HostID      string           `json:"host_id,omitempty"`
	ClassID     string           `json:"class_id,omitempty"`
	KeyKind     string           `json:"key_kind,omitempty"`
	Label       string           `json:"label"`
	Covers      int              `json:"covers"` // sensors (item, kind) or hosts (host, class) it reaches now
	Config      actionConfig     `json:"config"`
	CooldownMin int              `json:"cooldown_min"`
	By          string           `json:"by,omitempty"`
	UpdatedAt   int64            `json:"updated_at"`
	Last        *store.ActionRun `json:"last,omitempty"`
	Setup       []string         `json:"setup,omitempty"` // the lines the hosts need (sudoers, AllowKey)
	SetupNote   string           `json:"setup_note,omitempty"`
}

// coverage is how many sensors (item and kind scopes) or hosts (host and class) an action reaches,
// and the names its sensors pass (units, containers, services), for the sudoers line.
func (s *Server) coverage(ctx context.Context, a store.Action) (int, []string) {
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		return 0, nil
	}
	cs, _ := s.st.DeviceClasses(ctx)
	n, names, seen := 0, []string{}, map[string]bool{}
	hosts := map[string]bool{}
	for _, r := range rows {
		match := false
		switch a.Scope {
		case "item":
			match = r.ItemID == a.ItemID
		case "kind":
			match = cs[r.HostID] == a.ClassID && actionKind(r.key) == a.KeyKind
		case "host":
			match = r.HostID == a.HostID
		case "class":
			match = cs[r.HostID] == a.ClassID
		}
		if !match {
			continue
		}
		if a.Scope == "host" || a.Scope == "class" {
			hosts[r.HostID] = true
			continue
		}
		if !seen[r.ItemID] {
			seen[r.ItemID] = true
			n++
		}
		if _, name := sensorTarget(r.key); name != "" && !seen["n:"+name] {
			seen["n:"+name] = true
			names = append(names, name)
		}
	}
	if a.Scope == "host" || a.Scope == "class" {
		n = len(hosts)
	}
	sort.Strings(names)
	return n, names
}

// actionSetup is what the hosts must allow for an action, as lines to paste, and a note.
func actionSetup(a store.Action, c actionConfig, sshUser string, names []string) ([]string, string) {
	if sshUser == "" {
		sshUser = "argus"
	}
	shown := names
	if len(shown) == 0 {
		shown = []string{"<the service>"}
	}
	switch {
	case a.Type == "service" && a.Via == "ssh":
		cmds := make([]string, 0, len(shown))
		for _, n := range shown {
			cmds = append(cmds, "/usr/bin/systemctl restart "+n)
		}
		return []string{sshUser + " ALL=(root) NOPASSWD: " + strings.Join(cmds, ", ")},
			"The monitoring login can't restart services. Add this line on each host (visudo -f /etc/sudoers.d/argus), for the services it watches:"
	case a.Type == "container":
		return nil, "The monitoring login restarts the container with docker restart: it can when it is allowed to run docker ps, which the Containers sensors need already."
	case a.Type == "command" && a.Via == "ssh":
		if rest, ok := strings.CutPrefix(c.Command, "sudo -n "); ok {
			return []string{sshUser + " ALL=(root) NOPASSWD: " + rest},
				"The command runs with sudo: add this line on each host (visudo -f /etc/sudoers.d/argus), with the program's full path:"
		}
		return nil, "The command runs as the monitoring login. One that needs root starts with sudo -n, and the editor then shows its sudoers line."
	case a.Type == "service" && a.Via == "agent" && c.OS == "windows":
		return []string{"AllowKey=system.run[*Restart-Service -DisplayName*]"},
			"The Zabbix agent refuses every command until it is allowed: add this line to the agent's config (zabbix_agent2.conf or zabbix_agentd.conf) and restart the agent."
	case a.Type == "service" && a.Via == "agent":
		cmds := make([]string, 0, len(shown))
		for _, n := range shown {
			cmds = append(cmds, "/usr/bin/systemctl restart "+n)
		}
		return []string{"AllowKey=system.run[*sudo -n systemctl restart *]", "zabbix ALL=(root) NOPASSWD: " + strings.Join(cmds, ", ")},
			"The Zabbix agent refuses every command until it is allowed, and runs as zabbix: add the first line to the agent's config and restart it, the second with visudo -f /etc/sudoers.d/argus."
	case a.Type == "command" && a.Via == "agent":
		return []string{"AllowKey=system.run[*" + c.Command + "*]"},
			"The Zabbix agent refuses every command until it is allowed: add this line to the agent's config and restart the agent."
	case a.Type == "netscaler":
		return nil, "The NetScaler login needs a command policy that may save the config (a read-only one can't)."
	}
	return nil, ""
}

func (s *Server) viewAction(ctx context.Context, a store.Action, ix *actionIndex) actionView {
	c := readConfig(a)
	n, names := s.coverage(ctx, a)
	v := actionView{ID: a.ID, Name: a.Name, Type: a.Type, TypeName: actionTypeName[a.Type], Via: a.Via, Scope: a.Scope,
		ItemID: a.ItemID, HostID: a.HostID, ClassID: a.ClassID, KeyKind: a.KeyKind, Label: a.Label, Covers: n,
		Config: c, CooldownMin: a.CooldownMin, By: a.ByName, UpdatedAt: a.UpdatedAt}
	if ix != nil {
		if r, ok := ix.last[a.ID]; ok {
			r := r
			v.Last = &r
		}
	}
	v.Setup, v.SetupNote = actionSetup(a, c, "", names)
	return v
}

// GET /api/actions (admin): every action, with what it reaches now and its last run. Puts back any
// of their Zabbix scripts that went missing (a Zabbix restored from backup, a script deleted there).
func (s *Server) handleListActions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	forgetActions()
	ix := loadActions(ctx, s.st)
	s.reconcileActionScripts(ctx, ix.all)
	out := make([]actionView, 0, len(ix.all))
	for _, a := range ix.all {
		out = append(out, s.viewAction(ctx, a, ix))
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": out, "max": actionsMax})
}

// reconcileActionScripts remakes an action's missing script and removes the Argus scripts no action
// owns. A failure only waits for the next look.
func (s *Server) reconcileActionScripts(ctx context.Context, as []store.Action) {
	have, err := s.zbx.ScriptsInMenu(ctx, zabbix.ScriptMenuPath)
	if err != nil {
		return
	}
	owned := map[string]bool{}
	for _, a := range as {
		if a.ScriptID != "" && have[a.ScriptID] != "" {
			owned[a.ScriptID] = true
			continue
		}
		a.ScriptID = ""
		if id, err := s.zbx.CreateScript(ctx, actionScript(a, readConfig(a))); err == nil {
			_ = s.st.SetActionScript(ctx, a.ID, id)
			owned[id] = true
		} else {
			s.logger.Warn("actions: could not make the Zabbix script", "action", a.ID, "err", err)
		}
	}
	var stray []string
	for id, name := range have {
		if !owned[id] && strings.HasPrefix(name, "Argus action ") {
			stray = append(stray, id)
		}
	}
	if len(stray) > 0 {
		_ = s.zbx.DeleteScripts(ctx, stray...)
	}
}

// actionReq is the editor's body.
type actionReq struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"`
	Via         string       `json:"via"`
	Scope       string       `json:"scope"`   // item | kind | host | class
	ItemID      string       `json:"item_id"` // item, kind: the sensor the editor was opened on
	HostID      string       `json:"host_id"`
	ClassID     string       `json:"class_id"` // class; kind when not opened on a sensor
	KeyKind     string       `json:"key_kind"` // kind when not opened on a sensor
	Config      actionConfig `json:"config"`
	Secret      *string      `json:"secret"` // webhook: the secret header's value (nil keeps it)
	NSUser      string       `json:"ns_user"`
	NSPassword  *string      `json:"ns_password"`
	CooldownMin int          `json:"cooldown_min"`
}

func cleanLine(s string, n int) (string, bool) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > n {
		return s, false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return s, false
		}
	}
	return s, true
}

var webhookMethods = map[string]bool{"POST": true, "PUT": true, "GET": true}

// webhookPlaceholders are the words a webhook body may carry, each filled in JSON-escaped.
var webhookPlaceholders = []string{"host", "address", "site", "sensor", "value", "state", "problem", "user"}

// checkAction validates an editor body into an action (without its scope fields). "" or why not.
func checkAction(req actionReq) (store.Action, actionConfig, string) {
	a := store.Action{Type: req.Type, Via: req.Via, CooldownMin: req.CooldownMin}
	name, ok := cleanLine(req.Name, actionNameMax)
	if !ok || name == "" {
		return a, actionConfig{}, "give the action a name, up to 60 characters"
	}
	a.Name = name
	vias, known := actionVias[req.Type]
	if !known {
		return a, actionConfig{}, "unknown type of action"
	}
	okVia := false
	for _, v := range vias {
		okVia = okVia || v == req.Via
	}
	if !okVia {
		return a, actionConfig{}, actionTypeName[req.Type] + " can't run that way"
	}
	if a.CooldownMin == 0 {
		a.CooldownMin = actionCooldownDef
	}
	if a.CooldownMin < 1 || a.CooldownMin > actionCooldownMax {
		return a, actionConfig{}, "the cool-down is 1 minute to 24 hours"
	}
	c := actionConfig{}
	switch req.Type {
	case "command":
		cmd := strings.TrimSpace(req.Config.Command)
		if cmd == "" || len([]rune(cmd)) > actionCommandMax || strings.ContainsAny(cmd, "\r\n\x00") {
			return a, c, "the command is one line, up to 500 characters"
		}
		if req.Via == "agent" && strings.Contains(cmd, "{$") {
			return a, c, "the command can't name a Zabbix macro: its value would reach the output"
		}
		c.Command = cmd
	case "webhook":
		u, err := url.Parse(strings.TrimSpace(req.Config.URL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(req.Config.URL) > 500 || u.User != nil {
			return a, c, "the webhook needs an http or https URL (no login in it), up to 500 characters"
		}
		c.URL = u.String()
		c.Method = strings.ToUpper(strings.TrimSpace(req.Config.Method))
		if c.Method == "" {
			c.Method = "POST"
		}
		if !webhookMethods[c.Method] {
			return a, c, "the method is POST, PUT or GET"
		}
		body := strings.TrimSpace(req.Config.Body)
		if body != "" {
			if len(body) > actionBodyMax {
				return a, c, "the body can be up to 1000 characters"
			}
			if !json.Valid([]byte(fillPlaceholders(body, nil))) {
				return a, c, "the body is not valid JSON (placeholders like {host} go inside quotes)"
			}
		}
		c.Body = body
		if h := strings.TrimSpace(req.Config.Header); h != "" {
			if !headerRe.MatchString(h) {
				return a, c, "the header name is letters, digits and dashes"
			}
			c.Header = h
		}
	case "service":
		c.OS = req.Config.OS
	}
	return a, c, ""
}

// fillPlaceholders puts the values in a webhook body, each JSON-escaped (without its quotes: the
// placeholders sit inside the body's own strings). nil fills them with "".
func fillPlaceholders(body string, vals map[string]string) string {
	for _, p := range webhookPlaceholders {
		v, _ := json.Marshal(vals[p])
		body = strings.ReplaceAll(body, "{"+p+"}", string(v[1:len(v)-1]))
	}
	return body
}

// scopeAction fills an action's scope from the request: the sensor it was opened on, a host, a class.
// "" or why not, with the HTTP status.
func (s *Server) scopeAction(ctx context.Context, r *http.Request, req actionReq, a *store.Action, c *actionConfig) (int, string) {
	a.Scope = req.Scope
	sensorBound := a.Type == "service" || a.Type == "container"
	switch req.Scope {
	case "item", "kind":
		var key, sensor, hostID, classID string
		if req.ItemID != "" {
			t, ok := s.runbookTarget(ctx, req.ItemID)
			if !ok || !s.hostInScope(ctx, scopeFrom(r), t.hostID) {
				return http.StatusNotFound, "sensor not found"
			}
			key, sensor, hostID, classID = t.key, t.sensor, t.hostID, t.classID
		} else if req.Scope == "kind" {
			key, classID = req.KeyKind, req.ClassID
		} else {
			return http.StatusBadRequest, "pick the sensor"
		}
		if sensorBound {
			kind, _ := sensorTarget(strings.ReplaceAll(key, "[*]", "[x]"))
			switch {
			case a.Type == "container" && kind != "container",
				a.Type == "service" && kind != "unit" && kind != "winsvc":
				return http.StatusBadRequest, actionTypeName[a.Type] + " works on a sensor that watches one: this one doesn't"
			case a.Type == "service" && kind == "winsvc" && a.Via != "agent":
				return http.StatusBadRequest, "a Windows service restarts through the host's Zabbix agent"
			}
			c.OS = "linux"
			if kind == "winsvc" {
				c.OS = "windows"
			}
		}
		if req.Scope == "item" {
			a.ItemID = req.ItemID
			a.Label = s.hostLabel(ctx, hostID) + " · " + sensor
			return 0, ""
		}
		cls, ok := provision.ClassByID(classID)
		if !ok {
			return http.StatusBadRequest, "this host has no class, so the action can be for this sensor only"
		}
		a.ClassID, a.KeyKind = classID, actionKind(key)
		a.Label = "every " + kindWord(a.KeyKind, sensor) + " sensor on " + cls.Label + " hosts"
		return 0, ""
	case "host":
		if sensorBound {
			return http.StatusBadRequest, actionTypeName[a.Type] + " needs the sensor that names it: pick this sensor or its kind"
		}
		if req.HostID == "" && req.ItemID != "" {
			if it, err := s.zbx.Item(ctx, req.ItemID); err == nil && it != nil {
				req.HostID = it.HostID
			}
		}
		if req.HostID == "" || !s.hostInScope(ctx, scopeFrom(r), req.HostID) {
			return http.StatusNotFound, "host not found"
		}
		a.HostID, a.Label = req.HostID, s.hostLabel(ctx, req.HostID)
		return 0, ""
	case "class":
		if sensorBound {
			return http.StatusBadRequest, actionTypeName[a.Type] + " needs the sensor that names it: pick this sensor or its kind"
		}
		if a.Type == "netscaler" {
			return http.StatusBadRequest, "a NetScaler action is for one host: its login is set on it"
		}
		if req.ClassID == "" {
			hostID := req.HostID
			if hostID == "" && req.ItemID != "" {
				if it, err := s.zbx.Item(ctx, req.ItemID); err == nil && it != nil {
					hostID = it.HostID
				}
			}
			if cs, err := s.st.DeviceClasses(ctx); err == nil && hostID != "" {
				req.ClassID = cs[hostID]
			}
		}
		cls, ok := provision.ClassByID(req.ClassID)
		if !ok {
			return http.StatusBadRequest, "pick a class"
		}
		a.ClassID, a.Label = req.ClassID, "every "+cls.Label+" host"
		return 0, ""
	}
	return http.StatusBadRequest, "pick where the action applies"
}

// POST /api/actions (admin) makes an action; PUT /api/actions/{id} rewrites one.
func (s *Server) handleSaveAction(w http.ResponseWriter, r *http.Request) {
	var req actionReq
	if !decode(w, r, &req) {
		return
	}
	var id int64
	if r.PathValue("id") != "" {
		var ok bool
		if id, ok = pathID(w, r); !ok {
			return
		}
	}
	a, c, why := checkAction(req)
	if why != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": why})
		return
	}
	if a.Type == "netscaler" && req.Scope != "host" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a NetScaler action is for one host: its login is set on it"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if code, why := s.scopeAction(ctx, r, req, &a, &c); why != "" {
		writeJSON(w, code, map[string]string{"error": why})
		return
	}
	all, err := s.st.Actions(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	var old *store.Action
	for i := range all {
		if all[i].ID == id {
			old = &all[i]
		} else if strings.EqualFold(all[i].Name, a.Name) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "another action has that name"})
			return
		}
	}
	if id != 0 && old == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such action"})
		return
	}
	if id == 0 && len(all) >= actionsMax {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Argus keeps up to 50 actions"})
		return
	}
	if old != nil {
		a.ScriptID = old.ScriptID
		c.HasSecret = readConfig(*old).HasSecret
	}
	if a.Type != "webhook" || c.Header == "" {
		c.HasSecret = false
	}
	if req.Secret != nil && a.Type == "webhook" && c.Header != "" {
		c.HasSecret = strings.TrimSpace(*req.Secret) != ""
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		a.ByName = userLabel(u)
	}
	cfg, _ := json.Marshal(c)
	a.Config, a.ID = string(cfg), id
	newID, err := s.st.SaveAction(ctx, a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	a.ID = newID
	// The secrets go to Zabbix before the script that names them.
	if a.Type == "webhook" && req.Secret != nil {
		if v := strings.TrimSpace(*req.Secret); v != "" && c.Header != "" {
			err = s.zbx.SetGlobalSecret(ctx, secretMacro(a.ID), v)
		} else {
			err = s.zbx.DeleteGlobalMacro(ctx, secretMacro(a.ID))
		}
	} else if old != nil && readConfig(*old).HasSecret && !c.HasSecret {
		err = s.zbx.DeleteGlobalMacro(ctx, secretMacro(a.ID))
	}
	if err == nil && a.Type == "netscaler" {
		err = s.setNetScalerLogin(ctx, a.HostID, req.NSUser, req.NSPassword)
	}
	if err == nil {
		sc := actionScript(a, c)
		if a.ScriptID != "" {
			if err = s.zbx.UpdateScript(ctx, sc); err != nil {
				a.ScriptID = "" // gone from Zabbix: make it again
			}
		}
		if a.ScriptID == "" {
			var sid string
			if sid, err = s.zbx.CreateScript(ctx, sc); err == nil {
				err = s.st.SetActionScript(ctx, a.ID, sid)
			}
		}
	}
	forgetActions()
	verb := "Added an action"
	if old != nil {
		verb = "Changed an action"
	}
	ch := store.Change{Category: "actions", Action: verb, Object: a.Name, Detail: actionTypeName[a.Type] + ", " + a.Label}
	if a.HostID != "" {
		ch.HostIDs = []string{a.HostID}
	}
	if old != nil {
		changeDiff(r, "name", old.Name, a.Name)
		changeDiff(r, "applies to", old.Label, a.Label)
		changeDiff(r, "cool-down", strconv.Itoa(old.CooldownMin)+" min", strconv.Itoa(a.CooldownMin)+" min")
		oc := readConfig(*old)
		changeDiff(r, "command", oc.Command, c.Command)
		changeDiff(r, "URL", oc.URL, c.URL)
	}
	noteChange(r, ch)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved in Argus, but Zabbix refused the script: " + s.errText(r, err)})
		return
	}
	saved, _ := s.st.ActionByID(ctx, a.ID)
	writeJSON(w, http.StatusOK, s.viewAction(ctx, saved, loadActions(ctx, s.st)))
}

// setNetScalerLogin writes a NetScaler host's login as its macros: {$NS.USER} as text, {$NS.PASSWORD}
// secret (nil or blank keeps it).
func (s *Server) setNetScalerLogin(ctx context.Context, hostID, user string, password *string) error {
	user = strings.TrimSpace(user)
	if user != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`).MatchString(user) {
		return errors.New("the NetScaler login is letters, digits and . _ @ -")
	}
	ms, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	set := func(macro, value string, typ int) error {
		for _, m := range ms {
			if m.Macro == macro {
				return s.zbx.UpdateHostMacro(ctx, m.MacroID, value, typ)
			}
		}
		return s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: macro, Value: value, Type: typ})
	}
	if user != "" {
		if err := set("{$NS.USER}", user, 0); err != nil {
			return err
		}
	}
	if password != nil && *password != "" {
		return set("{$NS.PASSWORD}", *password, 1)
	}
	return nil
}

// DELETE /api/actions/{id} (admin): the action, its script and its secret.
func (s *Server) handleDeleteAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a, err := s.st.ActionByID(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such action"})
		return
	}
	if a.ScriptID != "" {
		if err := s.zbx.DeleteScripts(ctx, a.ScriptID); err != nil {
			s.logger.Warn("actions: could not delete the Zabbix script (it goes at the next look)", "action", id, "err", err)
		}
	}
	if readConfig(a).HasSecret {
		_ = s.zbx.DeleteGlobalMacro(ctx, secretMacro(id))
	}
	if err := s.st.DeleteAction(ctx, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	forgetActions()
	noteChange(r, store.Change{Category: "actions", Action: "Deleted an action", Object: a.Name, Detail: actionTypeName[a.Type] + ", " + a.Label})
	w.WriteHeader(http.StatusNoContent)
}

// ---- running ----

// actionTarget is where a run goes: a sensor (and its host) or a host.
type actionTarget struct {
	hostID, hostName, itemID, key, sensor, address, value, state, problem, site string
	proxyID, source                                                             string // source: the probe's name or "Core server"
}

func (s *Server) resolveActionTarget(ctx context.Context, r *http.Request, itemID, hostID string) (actionTarget, int, string) {
	var t actionTarget
	if itemID != "" {
		it, err := s.zbx.Item(ctx, itemID)
		if err != nil || it == nil {
			return t, http.StatusNotFound, "sensor not found"
		}
		t.itemID, t.hostID, t.key, t.sensor, t.value = itemID, it.HostID, it.Key, it.Name, strings.TrimSpace(it.LastValue+" "+it.Units)
		if row, ok, _ := s.censusRowByKey(ctx, itemID); ok {
			if row.Label != "" {
				t.sensor = row.Label
			}
			t.state, t.problem = row.State, row.Reason
		}
	} else {
		t.hostID = hostID
	}
	if t.hostID == "" || !s.hostInScope(ctx, scopeFrom(r), t.hostID) {
		return t, http.StatusNotFound, "host not found"
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return t, http.StatusBadGateway, "could not reach Zabbix"
	}
	h, ok := idx[t.hostID]
	if !ok {
		return t, http.StatusNotFound, "host not found"
	}
	t.hostName, t.proxyID, t.source = h.Name, h.ProxyID, "Core server"
	for _, g := range h.Groups {
		if site, ok := strings.CutPrefix(g, "Site/"); ok && t.site == "" {
			t.site = site
		}
	}
	if t.proxyID != "0" {
		if ps, err := s.zbx.Proxies(ctx); err == nil {
			for _, p := range ps {
				if p.ProxyID == t.proxyID {
					t.source = p.Name
				}
			}
		}
	}
	if ips, err := s.zbx.HostIPs(ctx); err == nil {
		t.address = ips[t.hostID]
	}
	return t, 0, ""
}

// applies reports whether an action is one the target offers.
func (s *Server) applies(ctx context.Context, a store.Action, t actionTarget) bool {
	forgetActions()
	ix := loadActions(ctx, s.st)
	for _, o := range ix.forSensor(t.hostID, t.itemID, t.key) {
		if o.ID == a.ID {
			return true
		}
	}
	return false
}

// actionInput is the manual input a run passes: the sensor's service or container, or the webhook's
// filled-in body (base64). why when the sensor's name can't pass.
func actionInput(a store.Action, c actionConfig, t actionTarget, user string) (input, target, why string) {
	switch a.Type {
	case "service", "container":
		kind, name := sensorTarget(t.key)
		if name == "" {
			return "", "", "this sensor doesn't name a service or a container"
		}
		re := unitRe
		switch kind {
		case "container":
			re = containerRe
		case "winsvc":
			re = winsvcRe
		}
		if !re.MatchString(name) {
			return "", name, "\"" + name + "\" has characters an action can't pass on"
		}
		return name, name, ""
	case "webhook":
		body := fillPlaceholders(c.Body, map[string]string{"host": t.hostName, "address": t.address, "site": t.site,
			"sensor": t.sensor, "value": t.value, "state": t.state, "problem": t.problem, "user": user})
		if len(body) > actionBodyMax {
			return "", "", "the webhook's body is over 1000 bytes with its values filled in"
		}
		return base64.StdEncoding.EncodeToString([]byte(body)), "", ""
	}
	return "", "", ""
}

// actionRun is one run, in memory while it lasts and 15 minutes after.
type actionRun struct {
	ID       int64
	ActionID int64
	Action   string
	HostID   string
	HostName string
	ItemID   string
	Sensor   string
	Source   string
	Remote   string
	State    string // running | checking | done | failed
	OK       bool
	Why      string
	Hint     []string // lines to paste (sudoers, AllowKey) when the host refused
	Output   string
	Check    string // what the sensor read after
	CheckOK  bool
	By       int64
	ByName   string
	Started  time.Time
	Ended    time.Time
}

type actionBoard struct {
	mu   sync.Mutex
	runs map[int64]*actionRun
}

func newActionBoard() *actionBoard { return &actionBoard{runs: map[int64]*actionRun{}} }

// add starts a run unless one is still going on the host.
func (b *actionBoard) add(r *actionRun) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, o := range b.runs {
		if (o.State == "done" || o.State == "failed") && time.Since(o.Ended) > actionRunKeep {
			delete(b.runs, id)
			continue
		}
		if o.HostID == r.HostID && (o.State == "running" || o.State == "checking") {
			return false
		}
	}
	for {
		r.ID = newRunID()
		if _, taken := b.runs[r.ID]; !taken && r.ID != 0 {
			break
		}
	}
	b.runs[r.ID] = r
	return true
}

func (b *actionBoard) update(id int64, f func(r *actionRun)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.runs[id]; ok {
		f(r)
	}
}

func (b *actionBoard) get(id int64) (actionRun, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[id]
	if !ok {
		return actionRun{}, false
	}
	return *r, true
}

// newRunID is a run's id: positive, under 2^53 (it survives the browser's numbers), unguessable.
func newRunID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) >> 11)
}

// actionPreview is what the Run dialog shows before anything runs.
type actionPreview struct {
	Action   string           `json:"action"`
	TypeName string           `json:"type_name"`
	Host     string           `json:"host"`
	Address  string           `json:"address,omitempty"`
	Sensor   string           `json:"sensor,omitempty"`
	Source   string           `json:"source"`
	Via      string           `json:"via"`
	Login    string           `json:"login,omitempty"`
	Remote   string           `json:"remote"`
	State    string           `json:"state,omitempty"`
	Since    int64            `json:"since,omitempty"`
	Maint    string           `json:"maintenance,omitempty"`
	Last     *store.ActionRun `json:"last,omitempty"`
	WaitSecs int64            `json:"wait_secs,omitempty"` // the cool-down left on this host
	Why      string           `json:"why,omitempty"`       // why it can't run (it still shows)
}

// preview is a run's dialog text, and why it can't run now ("" when it can).
func (s *Server) preview(ctx context.Context, a store.Action, t actionTarget) actionPreview {
	c := readConfig(a)
	_, target, why := actionInput(a, c, t, "")
	p := actionPreview{Action: a.Name, TypeName: actionTypeName[a.Type], Host: t.hostName, Address: t.address, Sensor: t.sensor,
		Source: t.source, Via: a.Via, Remote: actionRemote(a, c, target), State: t.state, Why: why}
	if a.Via == "ssh" {
		p.Login = "root"
		if ms, err := s.zbx.HostMacros(ctx, t.hostID); err == nil {
			for _, m := range ms {
				if m.Macro == "{$SSH.USER}" && m.Value != "" {
					p.Login = m.Value
				}
			}
		}
	}
	if t.itemID != "" {
		if row, ok, _ := s.censusRowByKey(ctx, t.itemID); ok {
			p.Since = row.Since
		}
	}
	if h, ok := s.maintenanceNow(ctx)[t.hostID]; ok {
		p.Maint = h.Name
	}
	if _, lastOn, err := s.st.LastActionRuns(ctx); err == nil {
		if r, ok := lastOn[store.ActionHostKey(a.ID, t.hostID)]; ok {
			r := r
			p.Last = &r
			if left := r.At + int64(a.CooldownMin)*60 - time.Now().Unix(); left > 0 {
				p.WaitSecs = left
				if p.Why == "" {
					p.Why = "it ran on " + t.hostName + " " + humanDur(time.Now().Unix()-r.At) + " ago: it can run there again in " + humanDur(left)
				}
			}
		}
	}
	return p
}

func (s *Server) loadRunTarget(w http.ResponseWriter, r *http.Request, itemID, hostID string) (store.Action, actionTarget, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Action{}, actionTarget{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a, err := s.st.ActionByID(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such action"})
		return a, actionTarget{}, false
	}
	t, code, why := s.resolveActionTarget(ctx, r, strings.TrimSpace(itemID), strings.TrimSpace(hostID))
	if why != "" {
		writeJSON(w, code, map[string]string{"error": why})
		return a, t, false
	}
	if !s.applies(ctx, a, t) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "\"" + a.Name + "\" doesn't apply to this sensor"})
		return a, t, false
	}
	return a, t, true
}

// GET /api/actions/{id}/preview?item_id=|host_id= (admin, helpdesk): what a run would do, where and
// as whom, and whether it can run now.
func (s *Server) handleActionPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a, t, ok := s.loadRunTarget(w, r, q.Get("item_id"), q.Get("host_id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.preview(ctx, a, t))
}

// GET /api/actions/for?item_id=|host_id= (any signed-in user): the actions a sensor or a host offers,
// for its ⋮ menu, and (for the editor opened there) the host's name and class. Viewers see none.
func (s *Server) handleActionsFor(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	out := []rowAction{}
	if u == nil || (u.Role != "admin" && u.Role != "helpdesk") {
		writeJSON(w, http.StatusOK, map[string]any{"actions": out})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	q := r.URL.Query()
	itemID, hostID, key := strings.TrimSpace(q.Get("item_id")), strings.TrimSpace(q.Get("host_id")), ""
	if itemID != "" {
		it, err := s.zbx.Item(ctx, itemID)
		if err != nil || it == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
			return
		}
		hostID, key = it.HostID, it.Key
	}
	if hostID == "" || !s.hostInScope(ctx, scopeFrom(r), hostID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if v := loadActions(ctx, s.st).rowView(hostID, itemID, key); v != nil {
		out = v
	}
	resp := map[string]any{"actions": out, "host": s.hostLabel(ctx, hostID)}
	if cs, err := s.st.DeviceClasses(ctx); err == nil {
		if c, ok := provision.ClassByID(cs[hostID]); ok {
			resp["class_id"], resp["class_label"] = c.ID, c.Label
		}
	}
	if itemID != "" {
		kind, _ := sensorTarget(key)
		resp["target"] = kind // unit | container | winsvc | "": which types fit this sensor
		resp["kind_word"] = kindWord(actionKind(key), "")
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/actions/{id}/run (admin, helpdesk): {item_id} or {host_id}. Starts the run and answers
// its id; the dialog reads GET /api/action-runs/{id} until it ends.
func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID string `json:"item_id"`
		HostID string `json:"host_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in"})
		return
	}
	limKey := "action:" + strconv.FormatInt(u.ID, 10)
	if blocked, retry := s.actionLimit.Blocked(limKey); blocked {
		writeThrottled(w, retry)
		return
	}
	a, t, ok := s.loadRunTarget(w, r, req.ItemID, req.HostID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p := s.preview(ctx, a, t)
	if p.Why != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": p.Why})
		return
	}
	if a.ScriptID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Zabbix has no script for this action yet: open Admin, Actions, so Argus makes it, then run it again"})
		return
	}
	c := readConfig(a)
	input, target, _ := actionInput(a, c, t, u.Email)
	run := &actionRun{ActionID: a.ID, Action: a.Name, HostID: t.hostID, HostName: t.hostName, ItemID: t.itemID, Sensor: t.sensor,
		Source: t.source, Remote: actionRemote(a, c, target), State: "running", By: u.ID, ByName: u.Email, Started: time.Now()}
	if !s.actionRuns.add(run) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an action is still running on " + t.hostName + "; try again when it ends"})
		return
	}
	s.actionLimit.Fail(limKey)
	skipChange(r) // the run's own entry is written when it ends, with how it went
	ip := s.clientIP(r)
	go s.runAction(run.ID, a, c, t, input, target, u, ip)
	writeJSON(w, http.StatusOK, map[string]any{"id": run.ID})
}

// actionResult is what argus_action.py prints: one JSON object.
type actionResult struct {
	OK     bool   `json:"ok"`
	Why    string `json:"why"`
	Hint   string `json:"hint"` // sudo | docker | login | missing
	Output string `json:"output"`
	State  string `json:"state"`
}

// runAction runs one action through Zabbix, then has the sensor read again, and logs it.
func (s *Server) runAction(id int64, a store.Action, c actionConfig, t actionTarget, input, target string, u *store.User, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), actionRunLimit+actionCheckFor+10*time.Second)
	defer cancel()
	start := time.Now()
	rctx, rcancel := context.WithTimeout(ctx, actionRunLimit)
	var in *string
	if actionScript(a, c).Validator != "" {
		in = &input
	}
	out, err := s.zbx.ExecuteScript(rctx, a.ScriptID, t.hostID, in)
	rcancel()
	res := s.readActionResult(a, c, t, target, out, err)
	took := time.Since(start)
	s.actionRuns.update(id, func(r *actionRun) {
		r.OK, r.Why, r.Output, r.Hint = res.OK, res.Why, res.Output, res.hints
		r.State = "failed"
		if res.OK {
			r.State = "checking"
			if t.itemID == "" {
				r.State = "done"
			}
		}
		if r.State != "checking" {
			r.Ended = time.Now()
		}
	})
	if res.OK && t.itemID != "" {
		check, checkOK := s.recheck(ctx, t, start)
		s.actionRuns.update(id, func(r *actionRun) {
			r.Check, r.CheckOK, r.State, r.Ended = check, checkOK, "done", time.Now()
		})
	}
	_ = s.st.AddActionRun(ctx, store.ActionRun{ActionID: a.ID, ActionName: a.Name, HostID: t.hostID, HostName: t.hostName, ItemID: t.itemID,
		Sensor: t.sensor, At: start.Unix(), OK: res.OK, Why: res.Why, ByName: u.Email, TookMS: took.Milliseconds()})
	forgetActions()
	pruneActionRunsDaily(ctx, s.st)
	how := "worked"
	if !res.OK {
		how = "failed: " + res.Why
	}
	object := a.Name + " on " + t.hostName
	if t.sensor != "" {
		object += " · " + t.sensor
	}
	ch := store.Change{At: start.Unix(), ActorKind: "user", ActorID: u.ID, Actor: u.Email, IP: ip, RequestID: newRequestID(),
		Category: "actions", Action: "Ran an action", Object: object, Detail: clip(how, 300), HostIDs: []string{t.hostID}}
	if t.itemID != "" {
		ch.ItemIDs = []string{t.itemID}
	}
	if err := s.addChanges(ctx, []store.Change{ch}); err != nil {
		s.logger.Warn("actions: could not log a run", "err", err)
	}
	// On the sensor's open incidents' timelines.
	if t.itemID != "" {
		if row, ok, _ := s.censusRowByKey(ctx, t.itemID); ok {
			for _, ev := range row.EventIDs {
				tlog(ctx, s.st, ev, "action", u.Email, "", a.Name+": "+how, start.Unix())
			}
		}
	}
}

type readResult struct {
	actionResult
	hints []string
}

// readActionResult turns what Zabbix answered into worked or failed, why in words, and the lines to
// paste when the host refused.
func (s *Server) readActionResult(a store.Action, c actionConfig, t actionTarget, target, out string, err error) readResult {
	var res readResult
	var se *zabbix.ScriptError
	switch {
	case errors.As(err, &se):
		res.Why = actionZabbixWhy(se.Text, t)
		res.Output = clip(se.Text, 8<<10)
	case errors.Is(err, context.DeadlineExceeded):
		res.Why = "no answer from Zabbix within " + strconv.Itoa(int(actionRunLimit.Seconds())) + " s"
	case err != nil:
		res.Why = "could not reach Zabbix: " + zabbix.UserMessage(err)
	case a.Via == "agent":
		res.Output = clip(out, 8<<10)
		res.OK, res.Why = agentOutcome(a, c, out)
	default:
		var ar actionResult
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &ar); jerr != nil {
			res.Why = "argus_action.py on " + t.source + " gave no answer Argus reads: update " + t.source
			res.Output = clip(out, 8<<10)
		} else {
			res.actionResult = ar
			res.Output = clip(ar.Output, 8<<10)
			res.Why = clip(printable(ar.Why), 300)
		}
	}
	if !res.OK && (res.Hint == "sudo" || strings.Contains(res.Output, "a password is required")) {
		names := []string{}
		if target != "" {
			names = []string{target}
		}
		res.hints, _ = actionSetup(a, c, s.sshLogin(t.hostID), names)
		if a.Type == "command" {
			if rest, ok := strings.CutPrefix(c.Command, "sudo -n "); ok {
				res.hints = []string{s.sshLogin(t.hostID) + " ALL=(root) NOPASSWD: " + rest}
			}
		}
	}
	if !res.OK && a.Via == "agent" && strings.Contains(res.Why, "AllowKey") {
		res.hints, _ = actionSetup(a, c, "", []string{target})
	}
	if !res.OK && res.Why == "" {
		res.Why = "it failed without saying why: its output is below"
	}
	return res
}

// sshLogin is a host's SSH login, for the lines a failed run shows.
func (s *Server) sshLogin(hostID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ms, err := s.zbx.HostMacros(ctx, hostID); err == nil {
		for _, m := range ms {
			if m.Macro == "{$SSH.USER}" && m.Value != "" {
				return m.Value
			}
		}
	}
	return "root"
}

// actionZabbixWhy is Zabbix's refusal in words a person acts on.
func actionZabbixWhy(text string, t actionTarget) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "remote commands are not enabled"):
		return t.source + " does not run actions yet: update it (probe/v7.0.32-r24 on turns on Zabbix's remote commands)"
	case strings.Contains(low, "global script execution on zabbix server is disabled"):
		return "the core's Zabbix server does not run actions yet: update the core (its update turns on EnableGlobalScripts) and restart it"
	case strings.Contains(low, "agent interface is not defined"):
		return t.hostName + " has no Zabbix agent interface: an agent action needs one"
	case strings.Contains(low, "agent is not available"):
		return "the Zabbix agent on " + t.hostName + " is not answering"
	case strings.Contains(low, "unsupported item key") || strings.Contains(low, "key access denied") || strings.Contains(low, "unknown metric"):
		return "the agent refused the command: add the AllowKey line below to its config"
	case strings.Contains(low, "argus_action.py") && (strings.Contains(low, "not found") || strings.Contains(low, "127")):
		return "argus_action.py is not on " + t.source + ": update it (a probe has it from probe/v7.0.32-r24 on; the core gets it from the argus-updater)"
	case strings.Contains(low, "does not match") || strings.Contains(low, "manual input"):
		return "Zabbix refused the value the action passes: " + clip(text, 200)
	case strings.Contains(low, "timeout") || strings.Contains(low, "timed out"):
		return "the run took too long: " + clip(text, 200)
	case strings.Contains(low, "no permissions to referred object"):
		return "Zabbix has no script for this action: open Admin, Actions, so Argus makes it again"
	}
	return clip(printable(text), 300)
}

// agentOutcome reads an agent command's output: a service restart checks the state it printed.
func agentOutcome(a store.Action, c actionConfig, out string) (bool, string) {
	o := strings.TrimSpace(out)
	if strings.Contains(o, "a password is required") {
		return false, "sudo asks for a password: the agent may not run this command"
	}
	if a.Type != "service" {
		return true, ""
	}
	if c.OS == "windows" {
		if strings.EqualFold(lastLine(o), "Running") {
			return true, ""
		}
		return false, "the service is not running after the restart: " + clip(lastLine(o), 200)
	}
	if strings.Contains(o, "ActiveState=active") {
		return true, ""
	}
	return false, "the service is not active after the restart: " + clip(strings.Join(strings.Fields(o), " "), 200)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// recheck asks Zabbix to read the sensor now and waits for the reading: what it reads and whether
// the sensor is out of trouble.
func (s *Server) recheck(ctx context.Context, t actionTarget, after time.Time) (string, bool) {
	if m, err := s.zbx.ReadingItems(ctx, []string{t.itemID}); err == nil {
		if rid := m[t.itemID]; rid != "" {
			_ = s.zbx.ExecuteNow(ctx, []string{rid})
		}
	}
	deadline := time.Now().Add(actionCheckFor)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(3 * time.Second):
		}
		it, err := s.zbx.Item(ctx, t.itemID)
		if err != nil || it == nil {
			continue
		}
		clock, _ := strconv.ParseInt(it.LastClock, 10, 64)
		if clock < after.Unix() {
			continue
		}
		secs := int(time.Since(after).Seconds())
		problem := false
		if trs, err := s.zbx.ItemTriggers(ctx, []string{t.itemID}); err == nil {
			for _, tr := range trs[t.itemID] {
				problem = problem || (tr.Status == 0 && tr.Value == 1)
			}
		}
		// A bare 1 or 0 (a service's "active") says nothing a person reads: leave it out.
		reads := ""
		if v := strings.TrimSpace(it.LastValue + " " + it.Units); v != "" && v != "0" && v != "1" {
			reads = " (it reads " + v + ")"
		}
		if problem {
			return fmt.Sprintf("%s is still in trouble, read again %d s after the run%s", t.sensor, secs, reads), false
		}
		return fmt.Sprintf("%s is out of trouble, read again %d s after the run%s", t.sensor, secs, reads), true
	}
	return t.sensor + " has not read again within " + strconv.Itoa(int(actionCheckFor.Seconds())) + " s: its next reading will tell", false
}

var actionRunsPrune struct {
	sync.Mutex
	last time.Time
}

func pruneActionRunsDaily(ctx context.Context, st *store.Store) {
	actionRunsPrune.Lock()
	due := time.Since(actionRunsPrune.last) > 24*time.Hour
	if due {
		actionRunsPrune.last = time.Now()
	}
	actionRunsPrune.Unlock()
	if due {
		_ = st.PruneActionRuns(ctx, time.Now().Add(-actionRunsKeep).Unix())
	}
}

// actionRunView is a run as the dialog reads it.
type actionRunView struct {
	ID       int64    `json:"id"`
	Action   string   `json:"action"`
	Host     string   `json:"host"`
	Sensor   string   `json:"sensor,omitempty"`
	Source   string   `json:"source"`
	Remote   string   `json:"remote"`
	State    string   `json:"state"`
	OK       bool     `json:"ok"`
	Why      string   `json:"why,omitempty"`
	Hint     []string `json:"hint,omitempty"`
	Output   string   `json:"output,omitempty"`
	Check    string   `json:"check,omitempty"`
	CheckOK  bool     `json:"check_ok,omitempty"`
	Started  int64    `json:"started"`
	Duration float64  `json:"duration"`
}

// GET /api/action-runs/{id} (admin, helpdesk): a run so far. Only the one who started it reads it
// (an admin reads any).
func (s *Server) handleGetActionRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, found := s.actionRuns.get(id)
	u, _ := auth.UserFrom(r.Context())
	if !found || u == nil || (run.By != u.ID && u.Role != "admin") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that run is gone (they are kept for 15 minutes)"})
		return
	}
	end := run.Ended
	if end.IsZero() {
		end = time.Now()
	}
	writeJSON(w, http.StatusOK, actionRunView{ID: run.ID, Action: run.Action, Host: run.HostName, Sensor: run.Sensor, Source: run.Source,
		Remote: run.Remote, State: run.State, OK: run.OK, Why: run.Why, Hint: run.Hint, Output: run.Output, Check: run.Check,
		CheckOK: run.CheckOK, Started: run.Started.Unix(), Duration: end.Sub(run.Started).Seconds()})
}
