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
	"time"

	"argus/internal/store"
)

// Site info: a site (a top-level group) keeps its address, who to call and its internet lines, each
// line tied to the sensor that measures it (a UniFi gateway's WAN, or a whole host such as the
// provider's modem). An alert about a line says who to call; a site whose probe stopped reporting
// lists every line, since the internet is the usual reason.

// siteOf is the site a host's alerts read under: the top of its first group.
func siteOf(groups []string) string {
	g := primarySite(groups)
	if i := strings.IndexByte(g, '/'); i >= 0 {
		g = g[:i]
	}
	return g
}

// wanOf is a UniFi WAN sensor's WAN: unifi.wan.avail[1] and unifi.wan.latency[1] are both WAN "1".
func wanOf(key string) string {
	if !strings.HasPrefix(key, "unifi.wan.") || !strings.HasSuffix(key, "]") {
		return ""
	}
	i := strings.IndexByte(key, '[')
	if i < 0 {
		return ""
	}
	return key[i+1 : len(key)-1]
}

// lineMatches reports whether a sensor (host + key) measures this internet line: the line's own
// sensor, any sensor of the same UniFi WAN, or any sensor of a line tied to the whole host.
func lineMatches(l store.SiteLine, hostID, key string) bool {
	if l.HostID == "" || l.HostID != hostID {
		return false
	}
	if l.Key == "" || l.Key == key {
		return true
	}
	w := wanOf(l.Key)
	return w != "" && w == wanOf(key)
}

// linesFor is the internet lines an alert on this sensor is about: the ones it measures, or all of
// them when it is the site's probe that stopped reporting.
func linesFor(info store.SiteInfo, hostID, key string) []store.SiteLine {
	if key == probeMasterKey {
		return info.Lines
	}
	var out []store.SiteLine
	for _, l := range info.Lines {
		if lineMatches(l, hostID, key) {
			out = append(out, l)
		}
	}
	return out
}

// lineCall is how an alert names who to call about a line: "Example Fiber +1 555 0100, circuit
// EXF-000123 (WAN 1)".
func lineCall(l store.SiteLine) string {
	who := strings.TrimSpace(l.Provider)
	if who == "" {
		who = "the provider"
	}
	if l.Phone != "" {
		who += " " + l.Phone
	}
	if l.Circuit != "" {
		who += ", circuit " + l.Circuit
	}
	if l.Name != "" {
		who += " (" + l.Name + ")"
	}
	return who
}

// firstContact is the site's contact alerts name: the first one with a name or a phone.
func firstContact(info store.SiteInfo) (store.SiteContact, bool) {
	for _, c := range info.Contacts {
		if c.Name != "" || c.Phone != "" {
			return c, true
		}
	}
	return store.SiteContact{}, false
}

// contactText reads "On-site IT: Bob Verdi +1 555 0101".
func contactText(c store.SiteContact) string {
	role := c.Role
	if role == "" {
		role = "Contact"
	}
	return strings.TrimSpace(role + ": " + strings.TrimSpace(c.Name+" "+c.Phone))
}

// callLines is what a "Who to call" channel adds to an alert: the lines it is about, then the site's
// contact.
func callLines(info store.SiteInfo, hostID, key string) []string {
	var out []string
	for _, l := range linesFor(info, hostID, key) {
		out = append(out, "Call "+lineCall(l))
	}
	if c, ok := firstContact(info); ok {
		out = append(out, contactText(c))
	}
	return out
}

// rowCall is the problem lists' who-to-call line on a sensor that measures an internet line:
// "Example Fiber · circuit EXF-000456 · support +1 555 0100".
func rowCall(lines []store.SiteLine) string {
	var parts []string
	for _, l := range lines {
		var p []string
		if l.Provider != "" {
			p = append(p, l.Provider)
		} else if l.Name != "" {
			p = append(p, l.Name)
		}
		if l.Circuit != "" {
			p = append(p, "circuit "+l.Circuit)
		}
		if l.Phone != "" {
			p = append(p, "support "+l.Phone)
		}
		if len(p) > 0 {
			parts = append(parts, strings.Join(p, " · "))
		}
	}
	return strings.Join(parts, "; ")
}

// markCalls gives a census problem row on a sensor that measures an internet line its who-to-call line.
func (s *Server) markCalls(ctx context.Context, rows []sensorRow) {
	infos, err := s.st.SiteInfos(ctx)
	if err != nil || len(infos) == 0 {
		return
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return
	}
	for i := range rows {
		r := &rows[i]
		if r.State == "ok" || r.State == "paused" || r.State == "hidden" {
			continue
		}
		info, ok := infos[siteOf(idx[r.HostID].Groups)]
		if !ok {
			continue
		}
		r.Call = rowCall(linesFor(info, r.HostID, r.key))
	}
}

// --- the API ---

type siteLineView struct {
	store.SiteLine
	HostName string `json:"host_name,omitempty"`
	Sensor   string `json:"sensor,omitempty"` // the sensor it is tied to, as the tree names it
	State    string `json:"state,omitempty"`  // up | down | "" (no sensor, or not reading)
}

type lineChoice struct {
	HostID string `json:"host_id"`
	Key    string `json:"key"`
	Label  string `json:"label"`
	Group  string `json:"group"` // "WAN sensors" | "Hosts"
}

type siteInfoView struct {
	Site      string              `json:"site"`
	Address   string              `json:"address"`
	Note      string              `json:"note"`
	Contacts  []store.SiteContact `json:"contacts"`
	Lines     []siteLineView      `json:"lines"`
	UpdatedAt int64               `json:"updated_at,omitempty"`
	Choices   []lineChoice        `json:"choices,omitempty"` // what a line can be tied to (the editor's)
}

// siteInfoView dresses a site's info for the UI: each line with its host, its sensor and whether it
// is up, read from the census.
func (s *Server) siteInfoView(ctx context.Context, info store.SiteInfo) siteInfoView {
	v := siteInfoView{Site: info.Site, Address: info.Address, Note: info.Note, Contacts: info.Contacts, Lines: []siteLineView{}, UpdatedAt: info.UpdatedAt}
	if len(info.Lines) == 0 {
		return v
	}
	idx, _ := s.hostIndex(ctx)
	rows, _ := s.sensorCensus(ctx)
	for _, l := range info.Lines {
		lv := siteLineView{SiteLine: l, HostName: idx[l.HostID].Name}
		for _, r := range rows {
			if r.HostID != l.HostID || (l.Key != "" && r.key != l.Key) || (l.Key == "" && r.key != defaultMasterKey) {
				continue
			}
			lv.Sensor = r.Name
			if r.Label != "" {
				lv.Sensor = r.Label
			}
			switch r.State {
			case "ok":
				lv.State = "up"
			case "error", "warning", "acked":
				lv.State = "down"
			}
			break
		}
		v.Lines = append(v.Lines, lv)
	}
	return v
}

// lineChoices is what a site's lines can be tied to: its hosts' UniFi WAN sensors, then its hosts.
func (s *Server) lineChoices(ctx context.Context, site string) []lineChoice {
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return nil
	}
	inSite := map[string]bool{}
	var hosts []lineChoice
	for id, h := range idx {
		for _, g := range h.Groups {
			if siteCovers(site, g) {
				inSite[id] = true
				hosts = append(hosts, lineChoice{HostID: id, Label: h.Name, Group: "Hosts"})
				break
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return naturalLess(hosts[i].Label, hosts[j].Label) })
	var wans []lineChoice
	if items, err := s.zbx.ItemsByKeyPrefix(ctx, "unifi.wan.avail["); err == nil {
		for _, it := range items {
			if !inSite[it.HostID] {
				continue
			}
			label := "WAN " + wanOf(it.Key) + " · " + idx[it.HostID].Name
			if i := strings.Index(it.Name, "("); i >= 0 && strings.HasSuffix(it.Name, ")") {
				label += " (" + it.Name[i+1:len(it.Name)-1] + ")"
			}
			wans = append(wans, lineChoice{HostID: it.HostID, Key: it.Key, Label: label, Group: "WAN sensors"})
		}
	}
	sort.Slice(wans, func(i, j int) bool { return naturalLess(wans[i].Label, wans[j].Label) })
	return append(wans, hosts...)
}

// siteParam is the site a request names, if it is a site (a top-level group) the user may see.
func siteParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	site := strings.TrimSpace(r.PathValue("site"))
	if site == "" || strings.Contains(site, "/") || len(site) > maxGroupNameLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a site is a top-level group"})
		return "", false
	}
	if !scopeFrom(r).showsGroup(site) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "site not found"})
		return "", false
	}
	return site, true
}

// GET /api/sites/{site}/info: a site's info, with what its lines can be tied to for an editor.
func (s *Server) handleSiteInfo(w http.ResponseWriter, r *http.Request) {
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	info, err := s.st.SiteInfoFor(ctx, site)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	v := s.siteInfoView(ctx, info)
	if r.URL.Query().Get("choices") == "1" && s.zbx.Authenticated() {
		v.Choices = s.lineChoices(ctx, site)
	}
	writeJSON(w, http.StatusOK, v)
}

// GET /api/sites/info: every site's info the user may see (the Monitoring tree's site rows).
func (s *Server) handleSiteInfos(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	infos, err := s.st.SiteInfos(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	sc := scopeFrom(r)
	out := []siteInfoView{}
	for site, info := range infos {
		if sc.showsGroup(site) {
			out = append(out, s.siteInfoView(ctx, info))
		}
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Site, out[j].Site) })
	writeJSON(w, http.StatusOK, out)
}

const (
	maxSiteContacts = 20
	maxSiteLines    = 10
	maxSiteField    = 200
)

// siteField trims a field and caps its length.
func siteField(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxSiteField {
		s = s[:maxSiteField]
	}
	return s
}

// PUT /api/sites/{site}/info: replace a site's info. Its editor writes the whole thing.
func (s *Server) handleSetSiteInfo(w http.ResponseWriter, r *http.Request) {
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	if !scopeFrom(r).coversGroup(site) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can only edit your own sites"})
		return
	}
	var req struct {
		Address  string              `json:"address"`
		Note     string              `json:"note"`
		Contacts []store.SiteContact `json:"contacts"`
		Lines    []store.SiteLine    `json:"lines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Contacts) > maxSiteContacts || len(req.Lines) > maxSiteLines {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "up to 20 contacts and 10 internet lines"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	info := store.SiteInfo{Site: site, Address: siteField(req.Address), Note: siteField(req.Note), Contacts: []store.SiteContact{}, Lines: []store.SiteLine{}}
	for _, c := range req.Contacts {
		c = store.SiteContact{Role: siteField(c.Role), Name: siteField(c.Name), Phone: siteField(c.Phone), Email: siteField(c.Email)}
		if c.Name != "" || c.Phone != "" || c.Email != "" {
			info.Contacts = append(info.Contacts, c)
		}
	}
	idx, _ := s.hostIndex(ctx)
	for _, l := range req.Lines {
		down, okD := lineMbps(l.DownMbps)
		up, okU := lineMbps(l.UpMbps)
		if !okD || !okU {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a line's speed is in Mbps, from 0 to 1000000"})
			return
		}
		l = store.SiteLine{Name: siteField(l.Name), HostID: strings.TrimSpace(l.HostID), Key: siteField(l.Key), Provider: siteField(l.Provider), Circuit: siteField(l.Circuit), Phone: siteField(l.Phone), Note: siteField(l.Note), DownMbps: down, UpMbps: up}
		if l.Name == "" && l.Provider == "" && l.Circuit == "" && l.Phone == "" && l.DownMbps == 0 {
			continue
		}
		if l.HostID == "" {
			l.Key = ""
		} else if h, ok := idx[l.HostID]; !ok || !inSite(site, h.Groups) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an internet line can only be tied to a host of this site"})
			return
		}
		info.Lines = append(info.Lines, l)
	}
	was, _ := s.st.SiteInfoFor(ctx, site)
	if err := s.st.SetSiteInfo(ctx, info); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	changeObject(r, site)
	siteInfoDiff(r, was, info, idx)
	writeJSON(w, http.StatusOK, s.siteInfoView(ctx, info))
}

// lineMbps checks a line's speed: Mbps, 0 (not given) to 1 Tbps, to the hundredth.
func lineMbps(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1_000_000 {
		return 0, false
	}
	return math.Round(v*100) / 100, true
}

// lineSpeed is a line's speed as people read it: "1000 Mbps", or "1000/300 Mbps" when the upload
// differs ("" when not given).
func lineSpeed(l store.SiteLine) string {
	if l.DownMbps <= 0 {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	if l.UpMbps > 0 && l.UpMbps != l.DownMbps {
		return f(l.DownMbps) + "/" + f(l.UpMbps) + " Mbps"
	}
	return f(l.DownMbps) + " Mbps"
}

// inSite reports whether any of these groups is in the site.
func inSite(site string, groups []string) bool {
	for _, g := range groups {
		if siteCovers(site, g) {
			return true
		}
	}
	return false
}

// siteInfoDiff logs what a site info edit changed.
func siteInfoDiff(r *http.Request, was, now store.SiteInfo, idx map[string]hostInfo) {
	changeDiff(r, "Address", was.Address, now.Address)
	changeDiff(r, "Note", was.Note, now.Note)
	contacts := func(cs []store.SiteContact) string {
		var out []string
		for _, c := range cs {
			out = append(out, contactText(c))
		}
		return strings.Join(out, "; ")
	}
	changeDiff(r, "Contacts", contacts(was.Contacts), contacts(now.Contacts))
	lines := func(ls []store.SiteLine) string {
		var out []string
		for _, l := range ls {
			t := lineCall(l)
			if l.HostID != "" {
				t += " on " + idx[l.HostID].Name
			}
			if sp := lineSpeed(l); sp != "" {
				t += ", " + sp
			}
			out = append(out, t)
		}
		return strings.Join(out, "; ")
	}
	changeDiff(r, "Internet lines", lines(was.Lines), lines(now.Lines))
}
