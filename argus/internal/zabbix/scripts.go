// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Global scripts for Argus actions (server/actions.go, DESIGN 7r): each action is one Zabbix script
// Argus owns, run with script.execute on a host. Zabbix runs it where the host is monitored: on the
// site's probe (its proxy, EnableRemoteCommands=1) or on the core's Zabbix server
// (EnableGlobalScripts=1), so probes and the core behave the same.

// Script is the part of a Zabbix script Argus sets.
type Script struct {
	ScriptID  string `json:"scriptid,omitempty"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	ExecuteOn int    `json:"execute_on,string"` // 0 the host's Zabbix agent, 2 the server or the proxy
	// Manual input: the value Argus passes with each run ({MANUALINPUT}), checked by Zabbix against
	// the validator before anything runs. Empty Validator: the script takes none.
	InputPrompt  string `json:"manualinput_prompt,omitempty"`
	Validator    string `json:"manualinput_validator,omitempty"`
	InputDefault string `json:"manualinput_default_value,omitempty"`
}

// ScriptMenuPath is where Argus's scripts sit in Zabbix's own host menu.
const ScriptMenuPath = "Argus"

func scriptParams(s Script) map[string]any {
	p := map[string]any{
		"name":        s.Name,
		"command":     s.Command,
		"type":        0, // a script (not IPMI, SSH, Telnet, webhook or URL)
		"scope":       2, // a manual host action
		"execute_on":  s.ExecuteOn,
		"menu_path":   ScriptMenuPath,
		"host_access": 3, // write: who may run it in Zabbix's own UI
		"description": "Made by Argus for one of its actions: change it in Argus (Admin, Actions), not here.",
		"manualinput": 0,
	}
	if s.Validator != "" {
		p["manualinput"] = 1
		p["manualinput_prompt"] = s.InputPrompt
		p["manualinput_validator_type"] = 0 // a regular expression
		p["manualinput_validator"] = s.Validator
		p["manualinput_default_value"] = s.InputDefault
	}
	return p
}

// CreateScript adds a script and answers its id.
func (c *Client) CreateScript(ctx context.Context, s Script) (string, error) {
	var out struct {
		IDs []string `json:"scriptids"`
	}
	if err := c.call(ctx, "script.create", scriptParams(s), true, &out); err != nil {
		return "", err
	}
	if len(out.IDs) == 0 {
		return "", errors.New("Zabbix made no script")
	}
	return out.IDs[0], nil
}

// UpdateScript rewrites a script in place.
func (c *Client) UpdateScript(ctx context.Context, s Script) error {
	p := scriptParams(s)
	p["scriptid"] = s.ScriptID
	return c.call(ctx, "script.update", p, true, nil)
}

// DeleteScripts removes scripts by id.
func (c *Client) DeleteScripts(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.call(ctx, "script.delete", ids, true, nil)
}

// ScriptsInMenu lists the scripts in a menu path (Argus's own), id -> name.
func (c *Client) ScriptsInMenu(ctx context.Context, menuPath string) (map[string]string, error) {
	var rows []struct {
		ID   string `json:"scriptid"`
		Name string `json:"name"`
		Menu string `json:"menu_path"`
	}
	if err := c.call(ctx, "script.get", map[string]any{"output": []string{"scriptid", "name", "menu_path"}, "filter": map[string]any{"menu_path": menuPath}}, true, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if strings.Trim(r.Menu, "/") == menuPath {
			out[r.ID] = r.Name
		}
	}
	return out, nil
}

// ScriptError is why Zabbix could not run a script, as Zabbix said it.
type ScriptError struct{ Text string }

func (e *ScriptError) Error() string { return e.Text }

// ExecuteScript runs a script on a host and answers what it printed. A run Zabbix refused or that
// failed is a *ScriptError with Zabbix's words; anything else is a transport or API error. Zabbix's
// frontend waits up to its script timeout (60 s by default), so this call waits longer than the
// client's usual 15 s. input is the manual input, nil for a script that takes none.
func (c *Client) ExecuteScript(ctx context.Context, scriptID, hostID string, input *string) (string, error) {
	p := map[string]any{"scriptid": scriptID, "hostid": hostID}
	if input != nil {
		p["manualinput"] = *input
	}
	var out struct {
		Response string `json:"response"`
		Value    string `json:"value"`
	}
	c.mu.RLock()
	hc := *c.http
	c.mu.RUnlock()
	hc.Timeout = 80 * time.Second
	err := c.callWith(ctx, &hc, "script.execute", p, true, &out)
	var re *rpcError
	if errors.As(err, &re) {
		why := strings.TrimSpace(re.Data)
		if why == "" {
			why = re.Message
		}
		return "", &ScriptError{Text: why}
	}
	if err != nil {
		return "", err
	}
	if out.Response != "" && out.Response != "success" {
		return out.Value, &ScriptError{Text: strings.TrimSpace(out.Value)}
	}
	return out.Value, nil
}

// GlobalMacro is a global user macro with its id.
type GlobalMacro struct {
	ID    string `json:"globalmacroid"`
	Macro string `json:"macro"`
}

// SetGlobalSecret writes a global secret macro, making it when it is missing. The value is never read
// back: Zabbix answers secret macros without their value.
func (c *Client) SetGlobalSecret(ctx context.Context, macro, value string) error {
	var rows []GlobalMacro
	if err := c.call(ctx, "usermacro.get", map[string]any{"output": []string{"globalmacroid", "macro"}, "globalmacro": true, "filter": map[string]any{"macro": macro}}, true, &rows); err != nil {
		return err
	}
	if len(rows) > 0 {
		return c.call(ctx, "usermacro.updateglobal", map[string]any{"globalmacroid": rows[0].ID, "value": value, "type": 1}, true, nil)
	}
	return c.call(ctx, "usermacro.createglobal", map[string]any{"macro": macro, "value": value, "type": 1, "description": "Made by Argus for one of its actions."}, true, nil)
}

// DeleteGlobalMacro removes a global macro by name, if it is there.
func (c *Client) DeleteGlobalMacro(ctx context.Context, macro string) error {
	var rows []GlobalMacro
	if err := c.call(ctx, "usermacro.get", map[string]any{"output": []string{"globalmacroid", "macro"}, "globalmacro": true, "filter": map[string]any{"macro": macro}}, true, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return c.call(ctx, "usermacro.deleteglobal", []string{rows[0].ID}, true, nil)
}
