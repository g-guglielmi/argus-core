// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/provision"
)

// Import from PRTG (DESIGN section 7h): Argus reads PRTG's device tree with an API key (probes,
// groups, devices with their addresses and tags, and the types of their sensors) and hands it to the
// browser, which maps PRTG's probes to Argus's and makes import rows of it. The key is used for these
// reads only and never kept; nothing changes in PRTG. PRTG's sensors aren't copied: each device gets
// the Argus class its sensors suggest, with that class's own sensors and thresholds.

type prtgDevice struct {
	Ref     string   `json:"ref"` // PRTG's object id
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Probe   string   `json:"probe"`
	Groups  []string `json:"groups"` // its group path below the probe, top first
	Tags    []string `json:"tags"`
	Sensors []string `json:"sensors"` // its sensors' types, as PRTG names them
	Class   string   `json:"class"`   // the Argus class they suggest ("" = pick one)
	Hint    string   `json:"hint"`    // why
}

type prtgProbe struct {
	Name    string `json:"name"`
	Devices int    `json:"devices"`
}

type prtgView struct {
	Version string       `json:"version,omitempty"`
	Probes  []prtgProbe  `json:"probes"`
	Groups  int          `json:"groups"`
	Tags    []string     `json:"tags"`
	Devices []prtgDevice `json:"devices"`
}

var prtgTitle = regexp.MustCompile(`(?is)<title>([^<]*)</title>`)

// prtgClient reads PRTG's table API.
type prtgClient struct {
	base string
	key  string
	http *http.Client
}

// prtgTable asks PRTG for one table (devices, groups, probenodes, sensors) with these columns.
func (c prtgClient) table(ctx context.Context, content, columns string) ([]map[string]any, string, error) {
	u := c.base + "/api/table.json?content=" + content + "&columns=" + columns + "&count=50000&apitoken=" + url.QueryEscape(c.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("could not reach PRTG: %s", prtgNetErr(err, c.key))
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, "", fmt.Errorf("PRTG refused the API key (HTTP %d): use a key with read access (PRTG 22.4 or later: Setup, Account settings, API keys)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		why := ""
		if m := prtgTitle.FindSubmatch(body); m != nil {
			why = " (" + strings.TrimSpace(string(m[1])) + ")"
		} else if len(body) > 0 && len(body) < 300 {
			why = " (" + strings.TrimSpace(string(body)) + ")"
		}
		return nil, "", fmt.Errorf("PRTG answered HTTP %d%s reading its %s", res.StatusCode, why, content)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", fmt.Errorf("PRTG's answer isn't data: the address should be PRTG's web interface, e.g. https://prtg.example.lan")
	}
	var ver string
	_ = json.Unmarshal(out["prtg-version"], &ver)
	var rows []map[string]any
	if raw, ok := out[content]; ok {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, "", fmt.Errorf("PRTG's %s list couldn't be read: %v", content, err)
		}
	}
	return rows, ver, nil
}

// prtgNetErr is a network error without the key in it (it travels in the address).
func prtgNetErr(err error, key string) string {
	msg := err.Error()
	if key != "" {
		msg = strings.ReplaceAll(msg, url.QueryEscape(key), "***")
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return msg
}

// str reads a PRTG field as text (a number comes as a number).
func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// readPRTG reads PRTG's device tree.
func readPRTG(ctx context.Context, c prtgClient) (prtgView, error) {
	v := prtgView{Probes: []prtgProbe{}, Tags: []string{}, Devices: []prtgDevice{}}
	devs, ver, err := c.table(ctx, "devices", "objid,device,host,probe,parentid,tags")
	if err != nil {
		return v, err
	}
	v.Version = ver
	groups, _, err := c.table(ctx, "groups", "objid,name,parentid")
	if err != nil {
		return v, err
	}
	probes, _, _ := c.table(ctx, "probenodes", "objid,name")
	sensors, _, err := c.table(ctx, "sensors", "objid,parentid,type")
	if err != nil {
		return v, err
	}
	type grp struct{ name, parent string }
	byID := map[string]grp{}
	for _, g := range groups {
		byID[str(g, "objid")] = grp{name: str(g, "name"), parent: str(g, "parentid")}
	}
	isProbe := map[string]bool{"0": true}
	for _, p := range probes {
		isProbe[str(p, "objid")] = true
	}
	types, raws := map[string][]string{}, map[string][]string{}
	for _, s := range sensors {
		p := str(s, "parentid")
		types[p] = append(types[p], str(s, "type"))
		if raw := str(s, "type_raw"); raw != "" {
			raws[p] = append(raws[p], raw)
		}
	}
	perProbe := map[string]int{}
	tagSet := map[string]bool{}
	groupSet := map[string]bool{}
	for _, d := range devs {
		dv := prtgDevice{Ref: str(d, "objid"), Name: str(d, "device"), Address: str(d, "host"), Probe: str(d, "probe"), Groups: []string{}, Tags: []string{}}
		// The group path: up from the device's parent to (not including) its probe.
		for id, n := str(d, "parentid"), 0; id != "" && !isProbe[id] && n < 20; n++ {
			g, ok := byID[id]
			if !ok || g.name == dv.Probe {
				break
			}
			dv.Groups = append([]string{strings.ReplaceAll(g.name, "/", "-")}, dv.Groups...)
			id = g.parent
		}
		if len(dv.Groups) > 0 {
			groupSet[dv.Probe+"/"+strings.Join(dv.Groups, "/")] = true
		}
		for _, t := range strings.Fields(str(d, "tags")) {
			dv.Tags = append(dv.Tags, t)
			tagSet[t] = true
		}
		seen := map[string]bool{}
		for _, t := range types[dv.Ref] {
			if t != "" && !seen[t] {
				seen[t] = true
				dv.Sensors = append(dv.Sensors, t)
			}
		}
		dv.Class, dv.Hint = guessPRTGClass(dv.Name, dv.Sensors, raws[dv.Ref])
		perProbe[dv.Probe]++
		v.Devices = append(v.Devices, dv)
	}
	for p, n := range perProbe {
		v.Probes = append(v.Probes, prtgProbe{Name: p, Devices: n})
	}
	sort.Slice(v.Probes, func(i, j int) bool { return naturalLess(v.Probes[i].Name, v.Probes[j].Name) })
	for t := range tagSet {
		v.Tags = append(v.Tags, t)
	}
	sort.Strings(v.Tags)
	v.Groups = len(groupSet)
	sort.SliceStable(v.Devices, func(i, j int) bool {
		a, b := v.Devices[i], v.Devices[j]
		if a.Probe != b.Probe {
			return naturalLess(a.Probe, b.Probe)
		}
		if ga, gb := strings.Join(a.Groups, "/"), strings.Join(b.Groups, "/"); ga != gb {
			return naturalLess(ga, gb)
		}
		return naturalLess(a.Name, b.Name)
	})
	return v, nil
}

// guessPRTGClass suggests the Argus class for a PRTG device from its sensors' types and its name, and
// says why. "" = no class fits well enough to guess: pick one.
func guessPRTGClass(name string, sensors, raw []string) (string, string) {
	all := strings.ToLower(strings.Join(append(append([]string{}, sensors...), raw...), " | "))
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(all, s) {
				return true
			}
		}
		return false
	}
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "unifi") || has("unifi"):
		if c := provision.SuggestUniFiClass("", low); c != "" {
			return c, "UniFi, by its name"
		}
		return "", "UniFi: pick switch, access point, gateway or console"
	case has("wmi", "windows", "hyper-v", "exchange", "active directory"):
		return "windows-snmp", "Windows sensors (Argus reads Windows over SNMP)"
	case has("ssh"):
		return "linux-ssh", "SSH sensors"
	case has("snmp linux", "snmplinux", "linux"):
		return "linux-snmp", "Linux SNMP sensors"
	case has("dns"):
		return "dns-server", "a DNS sensor"
	case has("ups"):
		return "", "a UPS: pick UPS (NUT) or Ping only"
	case has("vmware", "esx", "vcenter"):
		return "", "a VMware host: no Argus class for it, pick one (Ping only works)"
	case len(sensors) == 0:
		return "base", "no sensors in PRTG"
	}
	onlyPing := true
	for _, s := range sensors {
		if !strings.HasPrefix(strings.ToLower(s), "ping") {
			onlyPing = false
			break
		}
	}
	if onlyPing {
		return "base", "only a Ping sensor"
	}
	kinds := sensors
	if len(kinds) > 3 {
		kinds = kinds[:3]
	}
	return "base", "no Argus class for " + strings.Join(kinds, ", ") + ": Ping only"
}

// POST /api/import/prtg: read PRTG's device tree. The key is used for these reads only.
func (s *Server) handleImportPRTG(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Key      string `json:"key"`
		Insecure bool   `json:"insecure"` // accept a self-signed certificate
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	base := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	pu, err := url.Parse(base)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the PRTG address starts with https:// (or http://)"})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an API key is required"})
		return
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if req.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the admin's explicit choice for a self-signed PRTG
	}
	c := prtgClient{base: base, key: key, http: &http.Client{Timeout: 90 * time.Second, Transport: tr}}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	v, err := readPRTG(ctx, c)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// UniFi devices the saved controllers know get their class from the controller.
	if l := s.unifiLookup(ctx); l != nil && len(l.byIP) > 0 {
		for i := range v.Devices {
			if c, model := l.classFor(v.Devices[i].Address); c != "" {
				v.Devices[i].Class, v.Devices[i].Hint = c, "UniFi controller: "+model
			}
		}
	}
	writeJSON(w, http.StatusOK, v)
}
