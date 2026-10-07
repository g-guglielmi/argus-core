// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Network maps (DESIGN section 7j): a probe's site drawn as a tree from what Argus already knows. Each
// host's upstream device (upstream.go, the one in effect) is a link; the UniFi port it hangs off gives
// the link its traffic, speed and state (the upstream's port sensors, else the device's own uplink
// ones), and a gateway's WANs lead up to the internet. It is all read from the sensor census Argus
// keeps in memory, so a map polls nothing of its own. A map is off until an admin turns it on for its
// probe: an off map is never built.

const (
	mapBusyAt   = 70      // a link this full (percent of its speed, the busier way) is busy
	mapStaleAge = 10 * 60 // a reading older than this is unknown: its device stopped reporting
	mapFactsTTL = 10 * time.Minute
	mapMaxPins  = 1000
)

// mapKinds are the network devices a map always draws, by class; any other host is drawn only when
// it is linked to something.
var mapKinds = map[string]string{"unifi-gateway": "gateway", "unifi-switch": "switch", "unifi-ap": "ap"}

type mapNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // internet | gateway | switch | ap | host
	Model     string `json:"model,omitempty"`
	IP        string `json:"ip,omitempty"`
	State     string `json:"state"` // ok | warning | error | down | paused
	Errors    int    `json:"errors,omitempty"`
	Warnings  int    `json:"warnings,omitempty"`
	DownSince int64  `json:"down_since,omitempty"`
	Clients   *int   `json:"clients,omitempty"`  // an access point's connected clients
	Provider  string `json:"provider,omitempty"` // the internet: the line's provider, from the site's info
}

type mapLink struct {
	From     string   `json:"from"` // the upstream device
	To       string   `json:"to"`
	Port     string   `json:"port,omitempty"`      // the upstream's port ("4"); the gateway's WAN for the internet
	PortName string   `json:"port_name,omitempty"` // the port's name on the controller
	Down     *float64 `json:"down,omitempty"`      // bits per second toward To
	Up       *float64 `json:"up,omitempty"`        // bits per second from To
	Speed    float64  `json:"speed,omitempty"`     // the port's link speed, bits per second (0 = unknown)
	Use      *int     `json:"use,omitempty"`       // percent of Speed, the busier way
	NoLink   bool     `json:"no_link,omitempty"`   // the port (or the WAN) reports no link
	Latency  *float64 `json:"latency,omitempty"`   // the internet: the WAN's latency, ms
	State    string   `json:"state,omitempty"`     // the internet: the worst state of the WAN's sensors
	Source   string   `json:"source,omitempty"`    // controller | manual: who said To hangs off From
	// Where the link's traffic chart is: the sensor the link was read from.
	ChartHost string `json:"chart_host,omitempty"`
	ChartItem string `json:"chart_item,omitempty"`
}

type mapView struct {
	Probe    string                  `json:"probe"`
	Site     string                  `json:"site"`
	Nodes    []mapNode               `json:"nodes"`
	Links    []mapLink               `json:"links"`
	Unplaced []hostRefView           `json:"unplaced"` // hosts whose upstream device Argus doesn't know
	Pins     map[string]store.MapPin `json:"pins"`
	At       int64                   `json:"at"`
}

// mapInput is what one site's map is drawn from.
type mapInput struct {
	probe, site string
	hosts       map[string]hostInfo // the probe's hosts the viewer sees
	classes     map[string]string
	eff         map[string]upstreamLink
	rows        []sensorRow
	facts       map[string]*deviceFacts
	lines       []store.SiteLine
	now         int64
}

// mapReadings indexes the census by host and item key.
type mapReadings map[string]map[string]sensorRow

func (m mapReadings) row(host, key string) (sensorRow, bool) {
	r, ok := m[host][key]
	return r, ok
}

// num is a fresh numeric reading; a stale one (its device stopped reporting) is unknown.
func (m mapReadings) num(host, key string, now int64) (float64, bool) {
	r, ok := m[host][key]
	if !ok || r.LastClock == 0 || now-r.LastClock > mapStaleAge {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func ptrF(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// linkUse is how full a link is, the busier way, in percent of its speed.
func linkUse(l *mapLink) {
	if l.Speed <= 0 || (l.Down == nil && l.Up == nil) {
		return
	}
	busiest := 0.0
	if l.Down != nil {
		busiest = *l.Down
	}
	if l.Up != nil && *l.Up > busiest {
		busiest = *l.Up
	}
	u := int(math.Round(busiest / l.Speed * 100))
	l.Use = &u
}

// buildSiteMap draws a site: its network devices, the hosts linked to them, the links between them
// with their traffic, and the internet above each gateway.
func buildSiteMap(in mapInput) mapView {
	v := mapView{Probe: in.probe, Site: in.site, Nodes: []mapNode{}, Links: []mapLink{}, Unplaced: []hostRefView{}, Pins: map[string]store.MapPin{}, At: in.now}
	rd := mapReadings{}
	for _, r := range in.rows {
		if _, ok := in.hosts[r.HostID]; !ok || r.key == "" {
			continue
		}
		if rd[r.HostID] == nil {
			rd[r.HostID] = map[string]sensorRow{}
		}
		rd[r.HostID][r.key] = r
	}
	kindOf := func(id string) string {
		if k, ok := mapKinds[in.classes[id]]; ok {
			return k
		}
		return "host"
	}
	internal := func(id string) bool {
		c, ok := provision.ClassByID(in.classes[id])
		return ok && c.Internal
	}

	// The links: each host's upstream device, when both are on this site.
	linked := map[string]bool{}
	for child, up := range in.eff {
		if _, ok := in.hosts[child]; !ok || internal(child) {
			continue
		}
		if _, ok := in.hosts[up.Host]; !ok || internal(up.Host) {
			continue
		}
		l := mapLink{From: up.Host, To: child, Port: up.Port, Source: up.Source}
		in.portLink(&l, rd)
		v.Links = append(v.Links, l)
		linked[child], linked[up.Host] = true, true
	}

	for id, h := range in.hosts {
		if internal(id) {
			continue
		}
		kind := kindOf(id)
		if kind == "host" && !linked[id] {
			v.Unplaced = append(v.Unplaced, hostRefView{ID: id, Name: h.Name})
			continue
		}
		n := mapNode{ID: id, Name: h.Name, Kind: kind}
		if f := in.facts[id]; f != nil {
			n.Model, n.IP = f.Model, f.IP
		}
		in.nodeState(&n, rd)
		if kind == "ap" {
			if c, ok := rd.num(id, "unifi.clients", in.now); ok {
				ci := int(c)
				n.Clients = &ci
			}
		}
		v.Nodes = append(v.Nodes, n)
		if kind == "gateway" {
			in.internet(&v, id, rd)
		}
	}

	sort.Slice(v.Nodes, func(i, j int) bool {
		return strings.ToLower(v.Nodes[i].Name) < strings.ToLower(v.Nodes[j].Name) || (strings.EqualFold(v.Nodes[i].Name, v.Nodes[j].Name) && v.Nodes[i].ID < v.Nodes[j].ID)
	})
	sort.Slice(v.Links, func(i, j int) bool {
		if v.Links[i].From != v.Links[j].From {
			return v.Links[i].From < v.Links[j].From
		}
		return v.Links[i].To < v.Links[j].To
	})
	sort.Slice(v.Unplaced, func(i, j int) bool { return strings.ToLower(v.Unplaced[i].Name) < strings.ToLower(v.Unplaced[j].Name) })
	return v
}

// portLink reads a link's traffic: from the upstream's port when it names one the upstream reports,
// else from the device's own uplink sensors. Down is toward the device: what the port sends.
func (in mapInput) portLink(l *mapLink, rd mapReadings) {
	if p := l.Port; p != "" {
		inRow, hasIn := rd.row(l.From, "unifi.port.in["+p+"]")
		outRow, hasOut := rd.row(l.From, "unifi.port.out["+p+"]")
		if hasIn || hasOut {
			l.Down = ptrF(rd.num(l.From, "unifi.port.out["+p+"]", in.now))
			l.Up = ptrF(rd.num(l.From, "unifi.port.in["+p+"]", in.now))
			if s, ok := rd.num(l.From, "unifi.port.speed["+p+"]", in.now); ok {
				l.Speed = s
			}
			if st, ok := rd.num(l.From, "unifi.port.state["+p+"]", in.now); ok && st == 0 {
				l.NoLink = true
			}
			chart := inRow
			if !hasIn {
				chart = outRow
			}
			if n := parenSuffix(chart.Name); n != "" && n != "Port "+p {
				l.PortName = n
			}
			l.ChartHost, l.ChartItem = l.From, chart.ItemID
			linkUse(l)
			return
		}
	}
	if r, ok := rd.row(l.To, "unifi.uplink.in"); ok {
		l.Down = ptrF(rd.num(l.To, "unifi.uplink.in", in.now))
		l.Up = ptrF(rd.num(l.To, "unifi.uplink.out", in.now))
		l.ChartHost, l.ChartItem = l.To, r.ItemID
	}
}

// nodeState is a device's state from its sensors: down while its ping is in error, else its worst
// sensor (errors and warnings counted), paused when its ping is.
func (in mapInput) nodeState(n *mapNode, rd mapReadings) {
	n.State = "ok"
	for _, r := range rd[n.ID] {
		switch r.State {
		case "error":
			n.Errors++
		case "warning":
			n.Warnings++
		}
	}
	ping, hasPing := rd.row(n.ID, defaultMasterKey)
	switch {
	case hasPing && ping.State == "error":
		n.State, n.DownSince = "down", ping.Since
	case hasPing && ping.State == "paused":
		n.State = "paused"
	case n.Errors > 0:
		n.State = "error"
	case n.Warnings > 0:
		n.State = "warning"
	}
}

// internet adds a gateway's WANs: one internet node above it per WAN, its link carrying the WAN's
// traffic (in is the download) and latency, named after the site's line on it.
func (in mapInput) internet(v *mapView, gw string, rd mapReadings) {
	wans := map[string]bool{}
	for key := range rd[gw] {
		if strings.HasPrefix(key, "unifi.wan.in[") || strings.HasPrefix(key, "unifi.wan.out[") {
			if w := wanOf(key); w != "" {
				wans[w] = true
			}
		}
	}
	ws := make([]string, 0, len(wans))
	for w := range wans {
		ws = append(ws, w)
	}
	sort.Strings(ws)
	for _, w := range ws {
		id := "wan:" + gw + ":" + w
		n := mapNode{ID: id, Name: "Internet", Kind: "internet", State: "ok"}
		for _, line := range in.lines {
			if lineMatches(line, gw, "unifi.wan.in["+w+"]") {
				n.Provider = strings.TrimSpace(line.Provider)
				break
			}
		}
		l := mapLink{From: id, To: gw, Port: w, Source: "controller"}
		l.Down = ptrF(rd.num(gw, "unifi.wan.in["+w+"]", in.now))
		l.Up = ptrF(rd.num(gw, "unifi.wan.out["+w+"]", in.now))
		l.Latency = ptrF(rd.num(gw, "unifi.wan.latency["+w+"]", in.now))
		if a, ok := rd.num(gw, "unifi.wan.avail["+w+"]", in.now); ok && a == 0 {
			l.NoLink = true
		}
		for key, r := range rd[gw] {
			if wanOf(key) != w {
				continue
			}
			if r.State == "error" || (r.State == "warning" && l.State != "error") {
				l.State = r.State
			}
		}
		if r, ok := rd.row(gw, "unifi.wan.in["+w+"]"); ok {
			l.ChartHost, l.ChartItem = gw, r.ItemID
		}
		v.Nodes = append(v.Nodes, n)
		v.Links = append(v.Links, l)
	}
}

// --- the Maps page ---

type mapBusiest struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
	Use  int    `json:"use"`
}

type mapSiteView struct {
	Probe   string      `json:"probe"`
	Site    string      `json:"site"`
	On      bool        `json:"on"`
	OnBy    string      `json:"on_by,omitempty"`
	OnAt    int64       `json:"on_at,omitempty"`
	Devices int         `json:"devices"` // network devices on the map
	Hosts   int         `json:"hosts"`   // other hosts on the map
	Down    int         `json:"down"`
	Busy    int         `json:"busy"`    // links at least mapBusyAt full
	NoLink  int         `json:"no_link"` // links whose port reports no link
	Busiest *mapBusiest `json:"busiest,omitempty"`
}

// summarize is a map's line on the Maps page.
func (sv *mapSiteView) summarize(v mapView) {
	names := map[string]string{}
	for _, n := range v.Nodes {
		names[n.ID] = n.Name
		switch n.Kind {
		case "gateway", "switch", "ap":
			sv.Devices++
		case "host":
			sv.Hosts++
		}
		if n.State == "down" {
			sv.Down++
		}
	}
	for _, l := range v.Links {
		if l.NoLink {
			sv.NoLink++
		}
		if l.Use == nil {
			continue
		}
		if *l.Use >= mapBusyAt {
			sv.Busy++
		}
		if sv.Busiest == nil || *l.Use > sv.Busiest.Use {
			sv.Busiest = &mapBusiest{From: names[l.From], To: names[l.To], Port: l.Port, Use: *l.Use}
		}
	}
}

// mapData is what every map of one request is drawn from.
type mapData struct {
	idx     map[string]hostInfo
	vis     map[string]bool
	classes map[string]string
	eff     map[string]upstreamLink
	rows    []sensorRow
	facts   map[string]*deviceFacts
	sites   map[string]store.SiteInfo
}

func (s *Server) loadMapData(ctx context.Context, sc siteScope) (mapData, error) {
	var d mapData
	var err error
	if d.idx, err = s.hostIndex(ctx); err != nil {
		return d, err
	}
	if d.vis, err = s.visibleHosts(ctx, sc); err != nil {
		return d, err
	}
	if d.rows, err = s.sensorCensus(ctx); err != nil {
		return d, err
	}
	d.classes, _ = s.st.DeviceClasses(ctx)
	d.sites, _ = s.st.SiteInfos(ctx)
	d.eff, _ = s.upstreams(ctx)
	d.facts = s.mapFacts(ctx)
	return d, nil
}

// input is one probe's map input: the hosts it monitors (and the server's own on its site).
func (d mapData) input(p zabbix.Proxy, now int64) mapInput {
	site := probeSite(p.Name)
	in := mapInput{probe: p.Name, site: site, hosts: map[string]hostInfo{}, classes: d.classes, eff: d.eff, rows: d.rows, facts: d.facts, lines: d.sites[site].Lines, now: now}
	for id, h := range d.idx {
		if d.vis != nil && !d.vis[id] {
			continue
		}
		if h.ProxyID == p.ProxyID || ((h.ProxyID == "0" || h.ProxyID == "") && inSite(site, h.Groups)) {
			in.hosts[id] = h
		}
	}
	return in
}

// mapFactCache keeps the devices' models and addresses for the maps: they rarely change, and reading
// them is several Zabbix calls.
type mapFactCache struct {
	mu    sync.Mutex
	at    time.Time
	facts map[string]*deviceFacts
}

func (s *Server) mapFacts(ctx context.Context) map[string]*deviceFacts {
	c := &s.mapFactCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.facts != nil && time.Since(c.at) < mapFactsTTL {
		return c.facts
	}
	if f, err := s.collectFacts(ctx); err == nil {
		c.facts, c.at = f, time.Now()
	}
	if c.facts == nil {
		return map[string]*deviceFacts{}
	}
	return c.facts
}

// handleMaps lists the probes the viewer sees, each with its map's line: off, or what is on it.
func (s *Server) handleMaps(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sc := scopeFrom(r)
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	settings, err := s.st.SiteMaps(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the maps"})
		return
	}
	now := time.Now().Unix()
	out := []mapSiteView{}
	var data *mapData
	for _, p := range proxies {
		if !sc.coversGroup(probeSite(p.Name)) {
			continue
		}
		m := settings[p.Name]
		sv := mapSiteView{Probe: p.Name, Site: probeSite(p.Name), On: m.On, OnBy: m.OnBy, OnAt: m.OnAt}
		if m.On {
			if data == nil { // only a map that is on is drawn
				d, err := s.loadMapData(ctx, sc)
				if err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
					return
				}
				data = &d
			}
			sv.summarize(buildSiteMap(data.input(p, now)))
		}
		out = append(out, sv)
	}
	writeJSON(w, http.StatusOK, out)
}

// mapProxy finds the probe a map path names, inside the viewer's sites.
func (s *Server) mapProxy(ctx context.Context, w http.ResponseWriter, r *http.Request) (zabbix.Proxy, bool) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" || !scopeFrom(r).coversGroup(probeSite(name)) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe not found"})
		return zabbix.Proxy{}, false
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return zabbix.Proxy{}, false
	}
	for _, p := range proxies {
		if p.Name == name {
			return p, true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe not found"})
	return zabbix.Proxy{}, false
}

// handleMap draws one probe's map, when it is on.
func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, ok := s.mapProxy(ctx, w, r)
	if !ok {
		return
	}
	m, err := s.st.SiteMapFor(ctx, p.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the map"})
		return
	}
	if !m.On {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This site's map is off: an admin can turn it on in Maps.", "off": true})
		return
	}
	data, err := s.loadMapData(ctx, scopeFrom(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	v := buildSiteMap(data.input(p, time.Now().Unix()))
	v.Pins = m.Pins
	writeJSON(w, http.StatusOK, v)
}

// handleSetMapOn turns a probe's map on or off (admins: a map on is one more page to draw).
func (s *Server) handleSetMapOn(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "say whether the map is on (enabled)"})
		return
	}
	p, ok := s.mapProxy(ctx, w, r)
	if !ok {
		return
	}
	was, err := s.st.SiteMapFor(ctx, p.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the map"})
		return
	}
	if was.On == *req.Enabled {
		skipChange(r)
		writeJSON(w, http.StatusOK, map[string]bool{"on": was.On})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	if err := s.st.SetSiteMapOn(ctx, p.Name, *req.Enabled, userLabel(u)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the map"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": *req.Enabled})
}

// handleSetMapLayout keeps the devices moved by hand on a probe's map; none puts them all back.
func (s *Server) handleSetMapLayout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var req struct {
		Pins map[string]store.MapPin `json:"pins"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	pins, ok := cleanPins(req.Pins)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "up to 1000 devices, each moved less than 20000 points"})
		return
	}
	p, ok := s.mapProxy(ctx, w, r)
	if !ok {
		return
	}
	if err := s.st.SetSiteMapPins(ctx, p.Name, pins); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the layout"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

// cleanPins checks the moved devices: a sane count, short ids, finite moves; a device not moved at all
// is dropped.
func cleanPins(in map[string]store.MapPin) (map[string]store.MapPin, bool) {
	if len(in) > mapMaxPins {
		return nil, false
	}
	out := map[string]store.MapPin{}
	for id, p := range in {
		if id == "" || len(id) > 120 {
			return nil, false
		}
		for _, v := range []float64{p.DX, p.DY} {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) >= 20000 {
				return nil, false
			}
		}
		p = store.MapPin{DX: math.Round(p.DX), DY: math.Round(p.DY)}
		if p.DX != 0 || p.DY != 0 {
			out[id] = p
		}
	}
	return out, true
}
