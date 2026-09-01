// Package zabbix is a minimal client for the Zabbix JSON-RPC API.
// APIVersion is an unauthenticated connectivity probe; the read methods
// (Hosts, Items, ActiveTriggers, …) authenticate with an API token.
package zabbix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	mu    sync.RWMutex // guards url/token so they can be reconfigured at runtime
	url   string
	token string
	http  *http.Client
}

func New(url, token string) *Client {
	return &Client{
		url:   url,
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Configure updates the endpoint and token in place. Because callers (server, notifier,
// sweeper) share one *Client, a settings change propagates to all of them at once.
func (c *Client) Configure(url, token string) {
	c.mu.Lock()
	c.url, c.token = url, token
	c.mu.Unlock()
}

func (c *Client) endpoint() (url, token string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.url, c.token
}

// Authenticated reports whether an API token is configured for read calls.
func (c *Client) Authenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token != ""
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("zabbix rpc error %d: %s (%s)", e.Code, e.Message, e.Data)
}

// call performs a JSON-RPC request. When withAuth is true, the configured API
// token is sent as a Bearer header (Zabbix 7.0 style); out may be nil.
func (c *Client) call(ctx context.Context, method string, params any, withAuth bool, out any) error {
	url, token := c.endpoint()
	if url == "" {
		return fmt.Errorf("the Zabbix API URL is not set (configure it in Settings or ARGUS_ZABBIX_API_URL)")
	}
	if withAuth && token == "" {
		return fmt.Errorf("the Zabbix API token is not set (configure it in Settings or ARGUS_ZABBIX_API_TOKEN)")
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d from Zabbix API", resp.StatusCode)
	}

	var rr rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if rr.Error != nil {
		return rr.Error
	}
	if out != nil {
		if err := json.Unmarshal(rr.Result, out); err != nil {
			return fmt.Errorf("unexpected result: %w", err)
		}
	}
	return nil
}

// APIVersion calls apiinfo.version (no auth). Used as the "can I reach Zabbix?" probe.
func (c *Client) APIVersion(ctx context.Context) (string, error) {
	var version string
	if err := c.call(ctx, "apiinfo.version", []any{}, false, &version); err != nil {
		return "", err
	}
	return version, nil
}

// --- read models ---

type HostGroup struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
	Hosts   int    `json:"-"` // host count, populated only by HostGroups()
}

type Host struct {
	HostID  string      `json:"hostid"`
	Name    string      `json:"name"`
	Status  string      `json:"status"`  // "0" monitored, "1" not monitored
	ProxyID string      `json:"proxyid"` // "0" = monitored by the server
	Groups  []HostGroup `json:"hostgroups"`
}

type Item struct {
	ItemID    string `json:"itemid"`
	HostID    string `json:"hostid"`
	Name      string `json:"name"`
	Key       string `json:"key_"`
	LastValue string `json:"lastvalue"`
	LastClock string `json:"lastclock"`
	Units     string `json:"units"`
	ValueType string `json:"value_type"`
	Status    string `json:"status"` // "0" enabled, "1" disabled
	State     string `json:"state"`  // "0" normal, "1" not supported
}

type Trigger struct {
	TriggerID string `json:"triggerid"`
	Priority  string `json:"priority"` // severity 0..5
	Value     string `json:"value"`    // "1" == problem
	Hosts     []struct {
		HostID string `json:"hostid"`
	} `json:"hosts"`
}

// Hosts returns all hosts, sorted by name, each with its host groups (used to build the
// site -> host -> sensor tree; site = host group).
func (c *Client) Hosts(ctx context.Context) ([]Host, error) {
	params := map[string]any{
		"output":           []string{"hostid", "name", "status", "proxyid"},
		"selectHostGroups": []string{"groupid", "name"},
		"sortfield":        "name",
	}
	var hosts []Host
	return hosts, c.call(ctx, "host.get", params, true, &hosts)
}

// HostIPs returns each host's primary IP address (the main interface, else any interface), keyed by
// host id. Best-effort and used only to make hosts searchable by IP; a host with no IP is omitted.
func (c *Client) HostIPs(ctx context.Context) (map[string]string, error) {
	params := map[string]any{
		"output":           []string{"hostid"},
		"selectInterfaces": []string{"ip", "main"},
	}
	var rows []struct {
		HostID     string `json:"hostid"`
		Interfaces []struct {
			IP   string `json:"ip"`
			Main string `json:"main"`
		} `json:"interfaces"`
	}
	if err := c.call(ctx, "host.get", params, true, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, h := range rows {
		for _, i := range h.Interfaces {
			if i.IP == "" {
				continue
			}
			if _, ok := out[h.HostID]; !ok || i.Main == "1" {
				out[h.HostID] = i.IP // prefer the main interface, else the first with an IP
			}
		}
	}
	return out, nil
}

type Proxy struct {
	ProxyID    string `json:"proxyid"`
	Name       string `json:"name"`
	LastAccess string `json:"lastaccess"`     // unix seconds, "0" if never
	Mode       string `json:"operating_mode"` // "0" active, "1" passive
	// Runtime fields Zabbix 7.0 reports for a connected proxy. Version is the proxy's Zabbix
	// version — either a dotted string ("7.0.29") or the packed int form ("70029"), depending on
	// the release; callers normalise it. Empty/"0" when the proxy has never connected.
	Version string `json:"version"`
}

// Proxies returns the configured Zabbix proxies (the per-site collectors) with their
// last-access time, so the Probes view can show which sites are actually reporting.
// Uses output:"extend" so it tolerates field-name differences across Zabbix versions
// (e.g. proxy name/lastaccess) rather than failing on an unknown output field.
func (c *Client) Proxies(ctx context.Context) ([]Proxy, error) {
	params := map[string]any{
		"output":    "extend",
		"sortfield": "name",
	}
	var ps []Proxy
	return ps, c.call(ctx, "proxy.get", params, true, &ps)
}

// EnsureActiveProxyCert registers (or updates) an active proxy that authenticates to the server
// with a client certificate, pinned by issuer + subject. Idempotent: if a proxy with this name
// already exists, its TLS settings are updated instead of failing. Requires a super-admin token.
func (c *Client) EnsureActiveProxyCert(ctx context.Context, name, issuerDN, subjectDN string) error {
	existing, err := c.proxyIDByName(ctx, name)
	if err != nil {
		return err
	}
	// operating_mode 0 = active (proxy dials the server). TLS values are Zabbix's bitmask:
	// 1 = no encryption, 2 = PSK, 4 = certificate. tls_accept = 4 (server accepts the proxy's
	// cert-authenticated connection); tls_connect = 1 (no encryption on the unused, server-
	// initiated direction - an active proxy is never connected TO).
	fields := map[string]any{
		"tls_accept":  4,
		"tls_connect": 1,
		"tls_issuer":  issuerDN,
		"tls_subject": subjectDN,
	}
	if existing != "" {
		fields["proxyid"] = existing
		return c.call(ctx, "proxy.update", fields, true, nil)
	}
	fields["name"] = name
	fields["operating_mode"] = 0
	var res struct {
		ProxyIDs []string `json:"proxyids"`
	}
	return c.call(ctx, "proxy.create", fields, true, &res)
}

// DeleteProxy removes a proxy by id. Zabbix refuses (and returns a descriptive error) if any host is
// still monitored by it, so the caller surfaces that message. Requires a super-admin token.
func (c *Client) DeleteProxy(ctx context.Context, id string) error {
	// proxy.delete takes a bare array of proxyids as its params.
	return c.call(ctx, "proxy.delete", []string{id}, true, nil)
}

// proxyIDByName returns the proxyid for a proxy name, or "" if none exists.
func (c *Client) proxyIDByName(ctx context.Context, name string) (string, error) {
	params := map[string]any{
		"output": []string{"proxyid", "name"},
		"filter": map[string]any{"name": []string{name}},
	}
	var ps []Proxy
	if err := c.call(ctx, "proxy.get", params, true, &ps); err != nil {
		return "", err
	}
	if len(ps) > 0 {
		return ps[0].ProxyID, nil
	}
	return "", nil
}

// Items returns the items of one host, sorted by name.
func (c *Client) Items(ctx context.Context, hostID string) ([]Item, error) {
	params := map[string]any{
		"output":    []string{"itemid", "hostid", "name", "key_", "lastvalue", "lastclock", "units", "value_type", "status", "state"},
		"hostids":   hostID,
		"sortfield": "name",
	}
	var items []Item
	return items, c.call(ctx, "item.get", params, true, &items)
}

type ItemHost struct {
	HostID string `json:"hostid"`
	Name   string `json:"name"`
	Status string `json:"status"` // "0" monitored, "1" disabled, "3" template
}

// ItemWithHosts is an item plus its owning host (name + status), for the cross-host census.
type ItemWithHosts struct {
	Item
	Hosts []ItemHost `json:"hosts"`
}

// AllItems returns every host item with its owning host, for the sensor census that powers the
// status summary and filtered lists. No `monitored` filter, so items on disabled (paused) hosts
// are included; template items are filtered out by the caller via host status.
func (c *Client) AllItems(ctx context.Context) ([]ItemWithHosts, error) {
	params := map[string]any{
		"output":      []string{"itemid", "hostid", "name", "key_", "lastvalue", "lastclock", "units", "value_type", "status", "state"},
		"selectHosts": []string{"hostid", "name", "status"},
		"sortfield":   "name",
	}
	var items []ItemWithHosts
	return items, c.call(ctx, "item.get", params, true, &items)
}

// TriggerFull is a trigger with everything the Triggers tab needs: its name, severity, current
// problem/enabled state, when it last changed state, and the host(s) + item(s) it references (so a
// multi-sensor trigger shows all the sensors its expression watches).
type TriggerFull struct {
	TriggerID   string `json:"triggerid"`
	Description string `json:"description"`
	Priority    string `json:"priority"`   // severity 0..5
	Status      string `json:"status"`     // "0" enabled
	Value       string `json:"value"`      // "1" == problem
	LastChange  string `json:"lastchange"` // unix time it entered its current state
	Hosts       []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"hosts"`
	Items []struct {
		ItemID string `json:"itemid"`
		Name   string `json:"name"`
		Key    string `json:"key_"`
	} `json:"items"`
}

// AllTriggers returns every enabled trigger on a monitored host, with its host(s) and item(s), so the
// UI can list the alert rules and which sensors each one watches (including multi-sensor triggers).
func (c *Client) AllTriggers(ctx context.Context) ([]TriggerFull, error) {
	params := map[string]any{
		"output":            []string{"triggerid", "description", "priority", "status", "value", "lastchange"},
		"selectHosts":       []string{"hostid", "name", "status"},
		"selectItems":       []string{"itemid", "name", "key_"},
		"monitored":         true,
		"expandDescription": true,
		"skipDependent":     true,
		"sortfield":         "description",
	}
	var triggers []TriggerFull
	return triggers, c.call(ctx, "trigger.get", params, true, &triggers)
}

// ActiveTriggers returns triggers currently in the problem state, with their host(s).
func (c *Client) ActiveTriggers(ctx context.Context) ([]Trigger, error) {
	params := map[string]any{
		"output":        []string{"triggerid", "priority", "value"},
		"selectHosts":   []string{"hostid"},
		"filter":        map[string]any{"value": 1},
		"monitored":     true,
		"skipDependent": true,
	}
	var triggers []Trigger
	return triggers, c.call(ctx, "trigger.get", params, true, &triggers)
}

// Item returns one item's metadata (used before fetching its history/trends).
func (c *Client) Item(ctx context.Context, itemID string) (*Item, error) {
	params := map[string]any{
		"output":  []string{"itemid", "hostid", "name", "key_", "units", "value_type"},
		"itemids": itemID,
	}
	var items []Item
	if err := c.call(ctx, "item.get", params, true, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("item %s not found", itemID)
	}
	return &items[0], nil
}

// ItemsByIDs returns the requested items keyed by item id (name, last value, units).
func (c *Client) ItemsByIDs(ctx context.Context, ids []string) (map[string]Item, error) {
	out := map[string]Item{}
	if len(ids) == 0 {
		return out, nil
	}
	params := map[string]any{
		"output":  []string{"itemid", "hostid", "name", "key_", "lastvalue", "units", "value_type"},
		"itemids": ids,
	}
	var items []Item
	if err := c.call(ctx, "item.get", params, true, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		out[it.ItemID] = it
	}
	return out, nil
}

type HistoryPoint struct {
	Clock string `json:"clock"`
	Value string `json:"value"`
}

// History returns raw stored values for an item within [from, to] (unix seconds).
// valueType must match the item's value_type (0 float, 3 unsigned) or Zabbix returns nothing.
func (c *Client) History(ctx context.Context, itemID string, valueType int, from, to int64) ([]HistoryPoint, error) {
	params := map[string]any{
		"output":    "extend",
		"itemids":   itemID,
		"history":   valueType,
		"time_from": from,
		"time_till": to,
		"sortfield": "clock",
		"sortorder": "ASC",
	}
	var pts []HistoryPoint
	return pts, c.call(ctx, "history.get", params, true, &pts)
}

type TrendPoint struct {
	Clock    string `json:"clock"`
	ValueMin string `json:"value_min"`
	ValueAvg string `json:"value_avg"`
	ValueMax string `json:"value_max"`
}

// Trends returns hourly min/avg/max aggregates for an item within [from, to] (unix seconds).
func (c *Client) Trends(ctx context.Context, itemID string, from, to int64) ([]TrendPoint, error) {
	params := map[string]any{
		"output":    "extend",
		"itemids":   itemID,
		"time_from": from,
		"time_till": to,
	}
	var pts []TrendPoint
	return pts, c.call(ctx, "trend.get", params, true, &pts)
}

// ItemValueTypes returns the value_type of each requested item (to route history.get correctly).
func (c *Client) ItemValueTypes(ctx context.Context, ids []string) (map[string]string, error) {
	params := map[string]any{"output": []string{"itemid", "value_type"}, "itemids": ids}
	var items []Item
	if err := c.call(ctx, "item.get", params, true, &items); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		m[it.ItemID] = it.ValueType
	}
	return m, nil
}

type HistoryPointM struct {
	ItemID string `json:"itemid"`
	Clock  string `json:"clock"`
	Value  string `json:"value"`
}

// HistoryMulti returns raw values for many items of the SAME value_type within [from, now], for
// the inline sparklines (one history.get for the whole batch).
func (c *Client) HistoryMulti(ctx context.Context, itemIDs []string, valueType int, from int64) ([]HistoryPointM, error) {
	params := map[string]any{
		"output":    []string{"itemid", "clock", "value"},
		"itemids":   itemIDs,
		"history":   valueType,
		"time_from": from,
		"sortfield": "clock",
		"sortorder": "ASC",
	}
	var pts []HistoryPointM
	return pts, c.call(ctx, "history.get", params, true, &pts)
}

type Problem struct {
	EventID      string `json:"eventid"`
	Name         string `json:"name"`
	Severity     string `json:"severity"`     // 0..5
	Clock        string `json:"clock"`        // unix seconds
	Acknowledged string `json:"acknowledged"` // "0" / "1"
	ObjectID     string `json:"objectid"`     // triggerid
}

// Problems returns the active (unresolved) problems on one host, newest first.
func (c *Client) Problems(ctx context.Context, hostID string) ([]Problem, error) {
	params := map[string]any{
		"output":    []string{"eventid", "name", "severity", "clock", "acknowledged", "objectid"},
		"hostids":   hostID,
		"sortfield": []string{"eventid"},
		"sortorder": "DESC",
	}
	var ps []Problem
	return ps, c.call(ctx, "problem.get", params, true, &ps)
}

// AllProblems returns every active (unresolved) problem across all hosts, newest first.
func (c *Client) AllProblems(ctx context.Context) ([]Problem, error) {
	params := map[string]any{
		"output":    []string{"eventid", "name", "severity", "clock", "acknowledged", "objectid"},
		"sortfield": []string{"eventid"},
		"sortorder": "DESC",
	}
	var ps []Problem
	return ps, c.call(ctx, "problem.get", params, true, &ps)
}

// TriggerTarget is the host(s) and item(s) a trigger references.
type TriggerTarget struct {
	Expression string `json:"expression"` // trigger expression (used to extract a threshold)
	Hosts      []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
		Status string `json:"status"` // "0" monitored, "1" disabled (paused)
	} `json:"hosts"`
	Items []struct {
		ItemID string `json:"itemid"`
	} `json:"items"`
}

// TriggerTargets maps trigger ids to their host(s) and item(s), for the cross-host problem list.
func (c *Client) TriggerTargets(ctx context.Context, triggerIDs []string) (map[string]TriggerTarget, error) {
	out := map[string]TriggerTarget{}
	if len(triggerIDs) == 0 {
		return out, nil
	}
	params := map[string]any{
		"output":      []string{"triggerid", "expression"},
		"selectHosts": []string{"hostid", "name", "status"},
		"selectItems": []string{"itemid"},
		"triggerids":  triggerIDs,
	}
	var ts []struct {
		TriggerID string `json:"triggerid"`
		TriggerTarget
	}
	if err := c.call(ctx, "trigger.get", params, true, &ts); err != nil {
		return nil, err
	}
	for _, t := range ts {
		out[t.TriggerID] = t.TriggerTarget
	}
	return out, nil
}

// TriggerItems maps trigger ids to the item ids they reference (for highlighting the sensor).
func (c *Client) TriggerItems(ctx context.Context, triggerIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(triggerIDs) == 0 {
		return out, nil
	}
	params := map[string]any{
		"output":      []string{"triggerid"},
		"selectItems": []string{"itemid"},
		"triggerids":  triggerIDs,
	}
	var ts []struct {
		TriggerID string `json:"triggerid"`
		Items     []struct {
			ItemID string `json:"itemid"`
		} `json:"items"`
	}
	if err := c.call(ctx, "trigger.get", params, true, &ts); err != nil {
		return nil, err
	}
	for _, t := range ts {
		for _, it := range t.Items {
			out[t.TriggerID] = append(out[t.TriggerID], it.ItemID)
		}
	}
	return out, nil
}

// SetItemEnabled enables (status 0) or disables (status 1) an item - the "Pause" action.
func (c *Client) SetItemEnabled(ctx context.Context, itemID string, enabled bool) error {
	status := 1
	if enabled {
		status = 0
	}
	return c.call(ctx, "item.update", map[string]any{"itemid": itemID, "status": status}, true, nil)
}

// SetHostEnabled enables (status 0) or disables (status 1) a host - the "Pause" action.
func (c *Client) SetHostEnabled(ctx context.Context, hostID string, enabled bool) error {
	status := 1
	if enabled {
		status = 0
	}
	return c.call(ctx, "host.update", map[string]any{"hostid": hostID, "status": status}, true, nil)
}

// AcknowledgeEvent acknowledges a Zabbix problem event, optionally adding a message.
func (c *Client) AcknowledgeEvent(ctx context.Context, eventID, message string) error {
	action := 2 // acknowledge
	params := map[string]any{"eventids": eventID}
	if strings.TrimSpace(message) != "" {
		action |= 4 // add message
		params["message"] = message
	}
	params["action"] = action
	return c.call(ctx, "event.acknowledge", params, true, nil)
}

// UnacknowledgeEvent removes the acknowledgement from a Zabbix problem event.
func (c *Client) UnacknowledgeEvent(ctx context.Context, eventID string) error {
	return c.call(ctx, "event.acknowledge", map[string]any{"eventids": eventID, "action": 16}, true, nil)
}

// --- host-group management (config writes; require a super-admin token) ---

// HostGroups lists every Zabbix host group with the number of hosts in each - the data behind the
// Monitoring tree's group management (including empty groups, which the host-derived tree omits).
func (c *Client) HostGroups(ctx context.Context) ([]HostGroup, error) {
	var raw []struct {
		GroupID string `json:"groupid"`
		Name    string `json:"name"`
		Hosts   string `json:"hosts"` // count string, from selectHosts:"count"
	}
	params := map[string]any{
		"output":      []string{"groupid", "name"},
		"selectHosts": "count",
		"sortfield":   "name",
	}
	if err := c.call(ctx, "hostgroup.get", params, true, &raw); err != nil {
		return nil, err
	}
	out := make([]HostGroup, len(raw))
	for i, g := range raw {
		n, _ := strconv.Atoi(g.Hosts)
		out[i] = HostGroup{GroupID: g.GroupID, Name: g.Name, Hosts: n}
	}
	return out, nil
}

// EnsureHostGroup creates a host group with this name if one doesn't already exist. Idempotent, so
// it's safe to call on every probe enrollment / backfill.
func (c *Client) EnsureHostGroup(ctx context.Context, name string) error {
	var existing []HostGroup
	if err := c.call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid"}, "filter": map[string]any{"name": []string{name}}}, true, &existing); err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	_, err := c.CreateHostGroup(ctx, name)
	return err
}

// CreateHostGroup creates a new host group and returns its id.
func (c *Client) CreateHostGroup(ctx context.Context, name string) (string, error) {
	var res struct {
		GroupIDs []string `json:"groupids"`
	}
	if err := c.call(ctx, "hostgroup.create", map[string]any{"name": name}, true, &res); err != nil {
		return "", err
	}
	if len(res.GroupIDs) == 0 {
		return "", fmt.Errorf("Zabbix returned no group id")
	}
	return res.GroupIDs[0], nil
}

// RenameHostGroup renames an existing host group.
func (c *Client) RenameHostGroup(ctx context.Context, groupID, name string) error {
	return c.call(ctx, "hostgroup.update", map[string]any{"groupid": groupID, "name": name}, true, nil)
}

// DeleteHostGroup deletes a host group. Zabbix refuses if it would leave any host without a group.
func (c *Client) DeleteHostGroup(ctx context.Context, groupID string) error {
	return c.call(ctx, "hostgroup.delete", []string{groupID}, true, nil)
}

// SetHostGroups replaces a host's full group membership. Zabbix requires at least one group.
func (c *Client) SetHostGroups(ctx context.Context, hostID string, groupIDs []string) error {
	groups := make([]map[string]string, len(groupIDs))
	for i, id := range groupIDs {
		groups[i] = map[string]string{"groupid": id}
	}
	return c.call(ctx, "host.update", map[string]any{"hostid": hostID, "groups": groups}, true, nil)
}

// --- host identity + interface management (config writes; require a super-admin token) ---

func atoiSafe(s string) int { n, _ := strconv.Atoi(s); return n }

// SNMPDetails holds a Zabbix SNMP interface's credentials (the interface `details` object).
type SNMPDetails struct {
	Version        int    `json:"version"` // 1, 2 (v2c), 3
	Community      string `json:"community"`
	Bulk           int    `json:"bulk"`
	SecurityName   string `json:"security_name"`
	SecurityLevel  int    `json:"security_level"` // 0 noAuthNoPriv, 1 authNoPriv, 2 authPriv
	AuthProtocol   int    `json:"auth_protocol"`
	AuthPassphrase string `json:"auth_passphrase"`
	PrivProtocol   int    `json:"priv_protocol"`
	PrivPassphrase string `json:"priv_passphrase"`
	ContextName    string `json:"context_name"`
}

// HostInterface is one network interface of a host (Argus edits Agent=1 and SNMP=2).
type HostInterface struct {
	InterfaceID string       `json:"interfaceid"`
	Type        int          `json:"type"`  // 1 agent, 2 snmp, 3 ipmi, 4 jmx
	Main        int          `json:"main"`  // 1 = default interface of its type
	UseIP       int          `json:"useip"` // 0 connect via DNS, 1 via IP
	IP          string       `json:"ip"`
	DNS         string       `json:"dns"`
	Port        string       `json:"port"`
	SNMP        *SNMPDetails `json:"snmp,omitempty"`
}

// HostDetail is a host's identity + interfaces, for the settings editor.
type HostDetail struct {
	HostID      string          `json:"hostid"`
	Host        string          `json:"host"` // technical name
	Name        string          `json:"name"` // visible name
	ProxyID     string          `json:"proxyid"`
	MonitoredBy int             `json:"monitored_by"` // 0 server, 1 proxy, 2 proxy group
	Interfaces  []HostInterface `json:"interfaces"`
}

// HostDetail fetches a host's names, proxy and interfaces. The Zabbix `details` field is polymorphic
// (`[]` for non-SNMP, `{…}` for SNMP), so it's decoded via RawMessage and parsed only when an object.
func (c *Client) HostDetail(ctx context.Context, hostID string) (*HostDetail, error) {
	var raw []struct {
		HostID      string `json:"hostid"`
		Host        string `json:"host"`
		Name        string `json:"name"`
		ProxyID     string `json:"proxyid"`
		MonitoredBy string `json:"monitored_by"`
		Interfaces  []struct {
			InterfaceID string          `json:"interfaceid"`
			Type        string          `json:"type"`
			Main        string          `json:"main"`
			UseIP       string          `json:"useip"`
			IP          string          `json:"ip"`
			DNS         string          `json:"dns"`
			Port        string          `json:"port"`
			Details     json.RawMessage `json:"details"`
		} `json:"interfaces"`
	}
	params := map[string]any{
		"output":           []string{"hostid", "host", "name", "status", "proxyid", "monitored_by"},
		"selectInterfaces": []string{"interfaceid", "type", "main", "useip", "ip", "dns", "port", "details"},
		"hostids":          hostID,
	}
	if err := c.call(ctx, "host.get", params, true, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("host not found")
	}
	h := raw[0]
	hd := &HostDetail{HostID: h.HostID, Host: h.Host, Name: h.Name, ProxyID: h.ProxyID, MonitoredBy: atoiSafe(h.MonitoredBy)}
	for _, i := range h.Interfaces {
		iface := HostInterface{InterfaceID: i.InterfaceID, Type: atoiSafe(i.Type), Main: atoiSafe(i.Main), UseIP: atoiSafe(i.UseIP), IP: i.IP, DNS: i.DNS, Port: i.Port}
		if iface.Type == 2 && len(i.Details) > 0 && i.Details[0] == '{' {
			var d struct {
				Version        string `json:"version"`
				Community      string `json:"community"`
				Bulk           string `json:"bulk"`
				SecurityName   string `json:"securityname"`
				SecurityLevel  string `json:"securitylevel"`
				AuthProtocol   string `json:"authprotocol"`
				AuthPassphrase string `json:"authpassphrase"`
				PrivProtocol   string `json:"privprotocol"`
				PrivPassphrase string `json:"privpassphrase"`
				ContextName    string `json:"contextname"`
			}
			if json.Unmarshal(i.Details, &d) == nil {
				iface.SNMP = &SNMPDetails{Version: atoiSafe(d.Version), Community: d.Community, Bulk: atoiSafe(d.Bulk), SecurityName: d.SecurityName, SecurityLevel: atoiSafe(d.SecurityLevel), AuthProtocol: atoiSafe(d.AuthProtocol), AuthPassphrase: d.AuthPassphrase, PrivProtocol: atoiSafe(d.PrivProtocol), PrivPassphrase: d.PrivPassphrase, ContextName: d.ContextName}
			}
		}
		hd.Interfaces = append(hd.Interfaces, iface)
	}
	return hd, nil
}

// UpdateHost sets a host's technical and visible name.
func (c *Client) UpdateHost(ctx context.Context, hostID, host, name string) error {
	return c.call(ctx, "host.update", map[string]any{"hostid": hostID, "host": host, "name": name}, true, nil)
}

// SetHostProxy sets which collector monitors a host: server (monitoredBy 0, proxyid cleared) or a
// specific proxy (monitoredBy 1 + proxyID). Proxy groups (2) aren't managed here.
func (c *Client) SetHostProxy(ctx context.Context, hostID string, monitoredBy int, proxyID string) error {
	params := map[string]any{"hostid": hostID, "monitored_by": monitoredBy}
	if monitoredBy == 1 {
		params["proxyid"] = proxyID
	} else {
		params["proxyid"] = "0"
	}
	return c.call(ctx, "host.update", params, true, nil)
}

// ifaceParams builds the hostinterface.create/update params for an interface, including the SNMP
// `details` object for type 2.
func ifaceParams(i HostInterface) map[string]any {
	p := map[string]any{"type": i.Type, "main": i.Main, "useip": i.UseIP, "ip": i.IP, "dns": i.DNS, "port": i.Port}
	if i.Type == 2 && i.SNMP != nil {
		d := map[string]any{"version": i.SNMP.Version, "bulk": i.SNMP.Bulk}
		if i.SNMP.Version == 3 {
			d["securityname"] = i.SNMP.SecurityName
			d["securitylevel"] = i.SNMP.SecurityLevel
			d["authprotocol"] = i.SNMP.AuthProtocol
			d["authpassphrase"] = i.SNMP.AuthPassphrase
			d["privprotocol"] = i.SNMP.PrivProtocol
			d["privpassphrase"] = i.SNMP.PrivPassphrase
			d["contextname"] = i.SNMP.ContextName
		} else {
			d["community"] = i.SNMP.Community
		}
		p["details"] = d
	}
	return p
}

// CreateHostInterface adds an interface to a host and returns its id.
func (c *Client) CreateHostInterface(ctx context.Context, hostID string, i HostInterface) (string, error) {
	p := ifaceParams(i)
	p["hostid"] = hostID
	var res struct {
		InterfaceIDs []string `json:"interfaceids"`
	}
	if err := c.call(ctx, "hostinterface.create", p, true, &res); err != nil {
		return "", err
	}
	if len(res.InterfaceIDs) == 0 {
		return "", fmt.Errorf("Zabbix returned no interface id")
	}
	return res.InterfaceIDs[0], nil
}

// UpdateHostInterface updates an existing interface (identified by InterfaceID).
func (c *Client) UpdateHostInterface(ctx context.Context, i HostInterface) error {
	p := ifaceParams(i)
	p["interfaceid"] = i.InterfaceID
	return c.call(ctx, "hostinterface.update", p, true, nil)
}

// DeleteHostInterface removes an interface. Zabbix refuses if items still reference it.
func (c *Client) DeleteHostInterface(ctx context.Context, interfaceID string) error {
	return c.call(ctx, "hostinterface.delete", []string{interfaceID}, true, nil)
}

// ItemsOnInterface returns the ids of items bound to an interface (Zabbix refuses to delete an
// interface while items reference it, so these must be moved first).
func (c *Client) ItemsOnInterface(ctx context.Context, interfaceID string) ([]string, error) {
	var items []struct {
		ItemID string `json:"itemid"`
	}
	params := map[string]any{"output": []string{"itemid"}, "interfaceids": interfaceID}
	if err := c.call(ctx, "item.get", params, true, &items); err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ItemID
	}
	return out, nil
}

// SetItemInterface moves an item to a different interface. Zabbix still enforces type compatibility
// (an Agent item can't move to an SNMP-only interface), returning an error the caller surfaces.
func (c *Client) SetItemInterface(ctx context.Context, itemID, interfaceID string) error {
	return c.call(ctx, "item.update", map[string]any{"itemid": itemID, "interfaceid": interfaceID}, true, nil)
}

// HostsByProxy returns the hosts monitored by a proxy with their interfaces (connection fields only,
// SNMP details omitted) - used to propagate a proxy's SNMP default to every inheriting interface.
func (c *Client) HostsByProxy(ctx context.Context, proxyID string) ([]HostDetail, error) {
	var raw []struct {
		HostID     string `json:"hostid"`
		Interfaces []struct {
			InterfaceID string `json:"interfaceid"`
			Type        string `json:"type"`
			Main        string `json:"main"`
			UseIP       string `json:"useip"`
			IP          string `json:"ip"`
			DNS         string `json:"dns"`
			Port        string `json:"port"`
		} `json:"interfaces"`
	}
	params := map[string]any{
		"output":           []string{"hostid"},
		"selectInterfaces": []string{"interfaceid", "type", "main", "useip", "ip", "dns", "port"},
		"proxyids":         proxyID,
	}
	if err := c.call(ctx, "host.get", params, true, &raw); err != nil {
		return nil, err
	}
	out := make([]HostDetail, 0, len(raw))
	for _, h := range raw {
		hd := HostDetail{HostID: h.HostID}
		for _, i := range h.Interfaces {
			hd.Interfaces = append(hd.Interfaces, HostInterface{InterfaceID: i.InterfaceID, Type: atoiSafe(i.Type), Main: atoiSafe(i.Main), UseIP: atoiSafe(i.UseIP), IP: i.IP, DNS: i.DNS, Port: i.Port})
		}
		out = append(out, hd)
	}
	return out, nil
}
