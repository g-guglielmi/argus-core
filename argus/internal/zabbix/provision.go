package zabbix

import (
	"context"
	"fmt"
	"strings"
)

// This file adds the config-write half of the client used to provision hosts from device-class
// templates (DESIGN §5, ROADMAP §C): import the class templates, resolve their ids, and create a
// monitored host wired to them. All require a super-admin token, like the other write methods.

// Template is a Zabbix template's identity (technical `host` name + visible `name`).
type Template struct {
	TemplateID string `json:"templateid"`
	Host       string `json:"host"`
	Name       string `json:"name"`
}

// Macro is a Zabbix user macro ({$NAME} = value) as set on a host or template. Argus uses host-level
// macros for per-host threshold overrides (the template carries the defaults).
type Macro struct {
	Macro string `json:"macro"`
	Value string `json:"value"`
}

// HostTag is a Zabbix host tag (used to stamp the Argus device class + provisioning source).
type HostTag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// Templates returns the templates whose technical name (`host`) is in names. Names with no match are
// simply absent from the result, so callers can diff to detect templates that aren't imported yet.
func (c *Client) Templates(ctx context.Context, names []string) ([]Template, error) {
	params := map[string]any{
		"output": []string{"templateid", "host", "name"},
		"filter": map[string]any{"host": names},
	}
	var ts []Template
	return ts, c.call(ctx, "template.get", params, true, &ts)
}

// TemplateIDsByName resolves template technical names to their ids, erroring if any requested name
// has no matching template. That makes host creation fail loudly ("import the templates first")
// rather than silently creating a host with no monitoring attached.
func (c *Client) TemplateIDsByName(ctx context.Context, names []string) (map[string]string, error) {
	ts, err := c.Templates(ctx, names)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]string, len(ts))
	for _, t := range ts {
		byName[t.Host] = t.TemplateID
	}
	var missing []string
	for _, n := range names {
		if _, ok := byName[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("templates not found in Zabbix (import them first): %s", strings.Join(missing, ", "))
	}
	return byName, nil
}

// ImportConfiguration imports a Zabbix export document (a template plus its items/triggers/LLD).
// createMissing+updateExisting across the template entities so re-importing an edited template
// updates it in place; deleteMissing on the child entities keeps a template's items/triggers in
// step with the file (an item removed from the YAML is removed from the template). format is
// "yaml" or "xml"; source is the document text.
func (c *Client) ImportConfiguration(ctx context.Context, format, source string) error {
	create := map[string]bool{"createMissing": true, "updateExisting": true}
	sync := map[string]bool{"createMissing": true, "updateExisting": true, "deleteMissing": true}
	rules := map[string]any{
		"template_groups":    create,
		"templates":          create,
		"valueMaps":          create,
		"items":              sync,
		"triggers":           sync,
		"discoveryRules":     sync,
		"graphs":             sync,
		"httptests":          sync,
		"templateLinkage":    map[string]bool{"createMissing": true, "deleteMissing": true},
		"templateDashboards": sync,
	}
	params := map[string]any{"format": format, "rules": rules, "source": source}
	return c.call(ctx, "configuration.import", params, true, nil)
}

// CreateHostParams describes a host to create with host.create.
type CreateHostParams struct {
	Host        string // technical name (unique in Zabbix)
	Name        string // visible name (defaults to Host when empty)
	GroupIDs    []string
	TemplateIDs []string
	Interfaces  []HostInterface
	Macros      []Macro
	MonitoredBy int    // 0 server (default), 1 proxy
	ProxyID     string // used when MonitoredBy == 1
	Tags        []HostTag
	Description string
}

// CreateHost creates a monitored host (host.create) and returns its id. Interfaces are encoded with
// the same ifaceParams the interface editor uses, so SNMP `details` are identical to a hand-made host.
func (c *Client) CreateHost(ctx context.Context, p CreateHostParams) (string, error) {
	name := p.Name
	if name == "" {
		name = p.Host
	}
	groups := make([]map[string]string, len(p.GroupIDs))
	for i, id := range p.GroupIDs {
		groups[i] = map[string]string{"groupid": id}
	}
	params := map[string]any{"host": p.Host, "name": name, "groups": groups}
	if len(p.TemplateIDs) > 0 {
		templates := make([]map[string]string, len(p.TemplateIDs))
		for i, id := range p.TemplateIDs {
			templates[i] = map[string]string{"templateid": id}
		}
		params["templates"] = templates
	}
	if len(p.Interfaces) > 0 {
		ifaces := make([]map[string]any, len(p.Interfaces))
		for i, iface := range p.Interfaces {
			ifaces[i] = ifaceParams(iface)
		}
		params["interfaces"] = ifaces
	}
	if len(p.Macros) > 0 {
		params["macros"] = p.Macros
	}
	if len(p.Tags) > 0 {
		params["tags"] = p.Tags
	}
	if p.Description != "" {
		params["description"] = p.Description
	}
	if p.MonitoredBy == 1 && p.ProxyID != "" {
		params["monitored_by"] = 1
		params["proxyid"] = p.ProxyID
	}
	var res struct {
		HostIDs []string `json:"hostids"`
	}
	if err := c.call(ctx, "host.create", params, true, &res); err != nil {
		return "", err
	}
	if len(res.HostIDs) == 0 {
		return "", fmt.Errorf("Zabbix returned no host id")
	}
	return res.HostIDs[0], nil
}

// HostIDByName returns the id of the host with this technical name, or "" if none exists (used to
// reject a duplicate before host.create rather than surfacing Zabbix's raw error).
func (c *Client) HostIDByName(ctx context.Context, host string) (string, error) {
	params := map[string]any{"output": []string{"hostid"}, "filter": map[string]any{"host": []string{host}}}
	var hs []struct {
		HostID string `json:"hostid"`
	}
	if err := c.call(ctx, "host.get", params, true, &hs); err != nil {
		return "", err
	}
	if len(hs) > 0 {
		return hs[0].HostID, nil
	}
	return "", nil
}

// SetHostMacros replaces a host's user macros (host.update `macros` replaces the full set), for
// per-host threshold overrides on top of the template defaults.
func (c *Client) SetHostMacros(ctx context.Context, hostID string, macros []Macro) error {
	return c.call(ctx, "host.update", map[string]any{"hostid": hostID, "macros": macros}, true, nil)
}

// EnsureHostGroupID returns the id of the host group with this name, creating it if missing. Like
// EnsureHostGroup but returns the id, which host.create needs.
func (c *Client) EnsureHostGroupID(ctx context.Context, name string) (string, error) {
	var existing []HostGroup
	if err := c.call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid"}, "filter": map[string]any{"name": []string{name}}}, true, &existing); err != nil {
		return "", err
	}
	if len(existing) > 0 {
		return existing[0].GroupID, nil
	}
	return c.CreateHostGroup(ctx, name)
}
