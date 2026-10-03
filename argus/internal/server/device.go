// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A host's Device tab and the Inventory page (DESIGN section 7e): the facts read from the device (its
// model, serial, firmware or OS, IP and MAC), the ones only a person knows (asset tag, location), the
// links to open it elsewhere, what uses it, and its journal.

// deviceFacts is what Argus reads about a device from its own sensors.
type deviceFacts struct {
	Model    string `json:"model,omitempty"`
	Serial   string `json:"serial,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	OS       string `json:"os,omitempty"`
	IP       string `json:"ip,omitempty"`
	MAC      string `json:"mac,omitempty"`
	ReadAt   int64  `json:"read_at,omitempty"`
	From     string `json:"from,omitempty"` // where the facts come from: "the UniFi controller", "SNMP", ...
	// Upgrade is the firmware the device's own controller offers ("" = current); upgradeKnown says
	// the controller answered, so the device is never compared with others of its model.
	Upgrade      string `json:"upgrade,omitempty"`
	upgradeKnown bool
}

// upgradeKey is the UniFi controller's answer to "is newer firmware available for this device?".
const upgradeKey = "unifi.firmware.upgrade"

// applyUpgrade records the controller's firmware answer: its item has reported, even with nothing to
// offer.
func applyUpgrade(f *deviceFacts, it zabbix.Item) {
	if it.Key != upgradeKey || atoi64(it.LastClock) == 0 {
		return
	}
	f.upgradeKnown = true
	f.Upgrade = strings.TrimSpace(it.LastValue)
}

// factKeys maps the items that carry a fact to it, with where it comes from.
var factKeys = map[string][2]string{
	"unifi.model":        {"model", "the UniFi controller"},
	"unifi.serial":       {"serial", "the UniFi controller"},
	"unifi.mac":          {"mac", "the UniFi controller"},
	"unifi.firmware":     {"firmware", "the UniFi controller"},
	"system.descr[snmp]": {"os", "SNMP"},
	"hass.version.core":  {"firmware", "Home Assistant"},
	"hass.version.os":    {"os", "Home Assistant"},
	"adguard.version":    {"firmware", "AdGuard Home"},
}

// factPrefixes are the discovered items that carry one (an XCP-ng pool's hypervisor versions).
var factPrefixes = map[string][2]string{
	"xcp.host.version[": {"firmware", "XCP-ng"},
}

var (
	winBuild = regexp.MustCompile(`Build (\d+)`)
	nonHex   = regexp.MustCompile(`[^0-9A-Fa-f]`)
	digitRun = regexp.MustCompile(`\d+`)
)

func factKeyList() []string {
	out := make([]string, 0, len(factKeys)+1)
	out = append(out, upgradeKey)
	for k := range factKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// osFromSysDescr shortens an SNMP sysDescr to the operating system it names: "Linux 6.1.0-21-amd64
// (Debian)", "Windows (build 22631)", or the description itself, clipped.
func osFromSysDescr(v string) string {
	v = strings.TrimSpace(v)
	f := strings.Fields(v)
	if len(f) >= 3 && f[0] == "Linux" {
		os := "Linux " + f[2]
		for _, d := range []string{"Debian", "Ubuntu", "Red Hat", "CentOS", "Rocky", "Alma", "SUSE", "Alpine", "Arch"} {
			if strings.Contains(v, d) {
				os += " (" + d + ")"
				break
			}
		}
		return os
	}
	if strings.Contains(v, "Windows") {
		if m := winBuild.FindStringSubmatch(v); m != nil {
			return "Windows (build " + m[1] + ")"
		}
		return "Windows"
	}
	if utf8.RuneCountInString(v) > 80 {
		v = string([]rune(v)[:80]) + "..."
	}
	return v
}

// displayMAC writes a MAC as 00:00:5E:00:53:21 ("" when it doesn't look like one).
func displayMAC(v string) string {
	h := strings.ToUpper(nonHex.ReplaceAllString(v, ""))
	if len(h) != 12 {
		return ""
	}
	return strings.Join([]string{h[0:2], h[2:4], h[4:6], h[6:8], h[8:10], h[10:12]}, ":")
}

// applyFact adds one item's reading to a host's facts.
func applyFact(f *deviceFacts, field, from, val string, clock int64) {
	val = strings.TrimSpace(val)
	if val == "" {
		return
	}
	switch field {
	case "model":
		f.Model = val
	case "serial":
		f.Serial = val
	case "mac":
		if m := displayMAC(val); m != "" {
			f.MAC = m
		}
	case "firmware":
		if f.Firmware == "" {
			f.Firmware = val
		} else if !strings.Contains(", "+f.Firmware+", ", ", "+val+", ") {
			f.Firmware += ", " + val // a pool's hypervisors on different versions
		}
	case "os":
		if from == "SNMP" {
			val = osFromSysDescr(val)
		} else if from == "Home Assistant" {
			val = "Home Assistant OS " + val
		}
		f.OS = val
	}
	if f.From == "" {
		f.From = from
	}
	if clock > f.ReadAt {
		f.ReadAt = clock
	}
}

// collectFacts reads every host's facts in a few calls: the fact items across all hosts, the hosts'
// addresses, and the MACs discovery saw for hosts adopted from it.
func (s *Server) collectFacts(ctx context.Context) (map[string]*deviceFacts, error) {
	out := map[string]*deviceFacts{}
	get := func(id string) *deviceFacts {
		f := out[id]
		if f == nil {
			f = &deviceFacts{}
			out[id] = f
		}
		return f
	}
	items, err := s.zbx.ItemsByKeys(ctx, factKeyList())
	if err != nil {
		return nil, err
	}
	for prefix := range factPrefixes {
		more, err := s.zbx.ItemsByKeyPrefix(ctx, prefix)
		if err != nil {
			return nil, err
		}
		items = append(items, more...)
	}
	for _, it := range items {
		fk, ok := factKeys[it.Key]
		if !ok {
			for p, v := range factPrefixes {
				if strings.HasPrefix(it.Key, p) {
					fk, ok = v, true
				}
			}
		}
		if ok {
			applyFact(get(it.HostID), fk[0], fk[1], it.LastValue, atoi64(it.LastClock))
		}
		if it.Key == upgradeKey {
			applyUpgrade(get(it.HostID), it)
		}
	}
	if ips, err := s.zbx.HostIPs(ctx); err == nil {
		for id, ip := range ips {
			get(id).IP = ip
		}
	}
	if macs, err := s.st.DiscoveredMACs(ctx); err == nil {
		for id, m := range macs {
			if f := get(id); f.MAC == "" {
				f.MAC = displayMAC(m)
			}
		}
	}
	return out, nil
}

// hostFacts reads one host's facts (its items, its address).
func (s *Server) hostFacts(ctx context.Context, hostID string, items []zabbix.Item) deviceFacts {
	var f deviceFacts
	for _, it := range items {
		fk, ok := factKeys[it.Key]
		if !ok {
			for p, v := range factPrefixes {
				if strings.HasPrefix(it.Key, p) {
					fk, ok = v, true
				}
			}
		}
		if ok {
			applyFact(&f, fk[0], fk[1], it.LastValue, atoi64(it.LastClock))
		}
		applyUpgrade(&f, it)
	}
	if ips, err := s.zbx.HostIPs(ctx); err == nil {
		f.IP = ips[hostID]
	}
	if f.MAC == "" {
		if macs, err := s.st.DiscoveredMACs(ctx); err == nil {
			f.MAC = displayMAC(macs[hostID])
		}
	}
	return f
}

// fwOlder compares two firmware or OS versions by their numbers: 7.0.50 < 7.1.26, "Debian 12" <
// "Debian 13". ok is false when either has no number to compare.
func fwOlder(a, b string) (less, ok bool) {
	pa, pb := digitRun.FindAllString(a, -1), digitRun.FindAllString(b, -1)
	if len(pa) == 0 || len(pb) == 0 {
		return false, false
	}
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y, true
		}
	}
	return len(pa) < len(pb), true
}

// --- links ---

var linkSchemes = []string{"http://", "https://", "ssh://", "rdp://", "vnc://", "telnet://"}
var linkVar = regexp.MustCompile(`\{(ip|name|host|mac|group|macro:[A-Z0-9_.]+)\}`)

// linkAllowed reports whether an address may become a link: a web page or a remote session.
func linkAllowed(u string) bool {
	low := strings.ToLower(u)
	for _, s := range linkSchemes {
		if strings.HasPrefix(low, s) && len(u) > len(s) {
			return !strings.ContainsAny(u, " \t\r\n\"'<>")
		}
	}
	return false
}

// checkLinkTemplate says what's wrong with a link's address as typed ("" = fine).
func checkLinkTemplate(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return "an address is required"
	}
	probe := linkVar.ReplaceAllString(u, "x")
	if strings.HasPrefix(u, "{macro:") {
		probe = "https://x" + probe[1:]
	}
	if strings.Contains(probe, "{") || strings.Contains(probe, "}") {
		return "unknown placeholder: use {ip}, {name}, {host}, {mac}, {group} or {macro:NAME}"
	}
	if !linkAllowed(probe) {
		return "a link starts with http://, https://, ssh://, rdp://, vnc:// or telnet:// (or a host macro holding one)"
	}
	return ""
}

// linkHost is what a link's placeholders read from.
type linkHost struct {
	ip, name, host, mac, group string
	macros                     map[string]string // non-secret host macros, by name without {$ }
}

// expandLink fills a link's placeholders in; ok is false when one has no value or the result isn't a
// link Argus opens.
func expandLink(tpl string, h linkHost) (string, bool) {
	ok := true
	out := linkVar.ReplaceAllStringFunc(tpl, func(m string) string {
		v := ""
		switch name := m[1 : len(m)-1]; {
		case name == "ip":
			v = h.ip
		case name == "mac":
			v = h.mac
		case name == "name":
			v = url.PathEscape(h.name)
		case name == "host":
			v = url.PathEscape(h.host)
		case name == "group":
			v = url.PathEscape(h.group)
		case strings.HasPrefix(name, "macro:"):
			v = strings.TrimRight(h.macros[strings.TrimPrefix(name, "macro:")], "/")
		}
		if v == "" {
			ok = false
		}
		return v
	})
	return out, ok && linkAllowed(out)
}

type linkView struct {
	ID    int64  `json:"id,omitempty"`
	Label string `json:"label"`
	URL   string `json:"url"`
	From  string `json:"from"` // class | host
}

// hostLinkSet is a host's links: the class ones filled in from it, then its own.
func (s *Server) hostLinkSet(ctx context.Context, hostID, classID string, h linkHost) []linkView {
	out := []linkView{}
	if tpls, err := s.st.LinkTemplates(ctx); err == nil {
		for _, t := range tpls {
			if len(t.Classes) > 0 && !containsStr(t.Classes, classID) {
				continue
			}
			if u, ok := expandLink(t.URL, h); ok {
				out = append(out, linkView{ID: t.ID, Label: t.Label, URL: u, From: "class"})
			}
		}
	}
	if own, err := s.st.HostLinks(ctx, hostID); err == nil {
		for _, l := range own {
			if u, ok := expandLink(l.URL, h); ok || (linkAllowed(l.URL) && !strings.Contains(l.URL, "{")) {
				if !ok {
					u = l.URL
				}
				out = append(out, linkView{ID: l.ID, Label: l.Label, URL: u, From: "host"})
			}
		}
	}
	return out
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// --- the Device tab ---

type usedByView struct {
	Groups      []string `json:"groups"`
	Probe       string   `json:"probe"`
	StatusPages []string `json:"status_pages"`
	Maintenance []string `json:"maintenance"`
	Channels    []string `json:"channels"`
}

type ownFactsView struct {
	AssetTag string `json:"asset_tag"`
	Location string `json:"location"`
}

type deviceView struct {
	Facts    deviceFacts   `json:"facts"`
	Own      ownFactsView  `json:"own"`
	Class    string        `json:"class,omitempty"`
	Links    []linkView    `json:"links"`
	Tags     []hostTag     `json:"tags"`
	UsedBy   usedByView    `json:"used_by"`
	Upstream upstreamView  `json:"upstream"`
	Site     *siteInfoView `json:"site,omitempty"` // its site's address, contacts and internet lines (siteinfo.go)
}

// GET /api/hosts/{id}/device: the host's Device tab.
func (s *Server) handleHostDevice(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id := r.PathValue("id")
	hd, err := s.zbx.HostDetail(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	items, _ := s.zbx.Items(ctx, id)
	out := deviceView{Facts: s.hostFacts(ctx, id, items), Links: []linkView{}, Tags: []hostTag{}}
	classID, _, _ := s.st.GetDeviceClass(ctx, id)
	if c, ok := provision.ClassByID(classID); ok {
		out.Class = c.Label
	}
	if own, err := s.st.HostFacts(ctx); err == nil {
		out.Own = ownFactsView{AssetTag: own[id].AssetTag, Location: own[id].Location}
	}
	idx, _ := s.hostIndex(ctx)
	hi := idx[id]
	if ti, err := s.hostTagIndex(ctx); err == nil && ti[id] != nil {
		out.Tags = ti[id]
	}
	lh := linkHost{ip: out.Facts.IP, name: hd.Name, host: hd.Host, mac: out.Facts.MAC, macros: map[string]string{}}
	if len(hi.Groups) > 0 {
		lh.group = hi.Groups[0]
	}
	if hm, err := s.zbx.HostMacros(ctx, id); err == nil {
		for _, m := range hm {
			if m.Type == 0 {
				lh.macros[strings.TrimSuffix(strings.TrimPrefix(m.Macro, "{$"), "}")] = m.Value
			}
		}
	}
	out.Links = s.hostLinkSet(ctx, id, classID, lh)
	out.UsedBy = s.usedBy(ctx, id, hi, tagNames(out.Tags))
	out.Upstream = s.hostUpstreamView(ctx, id)
	if site := siteOf(hi.Groups); site != "" {
		if info, err := s.st.SiteInfoFor(ctx, site); err == nil && !info.Empty() {
			v := s.siteInfoView(ctx, info)
			out.Site = &v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// usedBy lists what a host is part of: its groups and probe, the status pages and maintenance windows
// that cover it, and the shared channels its alerts reach.
func (s *Server) usedBy(ctx context.Context, id string, h hostInfo, tags []string) usedByView {
	u := usedByView{Groups: h.Groups, StatusPages: []string{}, Maintenance: []string{}, Channels: []string{}}
	if u.Groups == nil {
		u.Groups = []string{}
	}
	u.Probe = s.proxyNames(ctx)[h.ProxyID]
	covers := func(sites []string) bool {
		if len(sites) == 0 {
			return true
		}
		for _, site := range sites {
			for _, g := range h.Groups {
				if siteCovers(site, g) {
					return true
				}
			}
		}
		return false
	}
	if pages, err := s.st.ListStatusPages(ctx); err == nil {
		for _, p := range pages {
			if covers(p.Sites) {
				u.StatusPages = append(u.StatusPages, p.Name)
			}
		}
	}
	if wins, err := s.st.MaintenanceWindows(ctx); err == nil {
		for _, m := range wins {
			if containsStr(m.HostIDs, id) || (len(m.Sites) > 0 && covers(m.Sites)) {
				u.Maintenance = append(u.Maintenance, m.Name)
			}
		}
	}
	if chans, err := s.st.EnabledNotifyChannels(ctx); err == nil {
		for _, c := range chans {
			if c.Alerts && channelMatches(c.Sites, 0, h.Groups, 5) && tagsMatch(c.Tags, tags) {
				u.Channels = append(u.Channels, c.Name)
			}
		}
	}
	return u
}

// --- the Inventory ---

type inventoryRow struct {
	HostID   string   `json:"host_id"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	Probe    string   `json:"probe"`
	ClassID  string   `json:"class_id,omitempty"`
	Class    string   `json:"class,omitempty"`
	AssetTag string   `json:"asset_tag,omitempty"`
	Location string   `json:"location,omitempty"`
	Newest   string   `json:"newest,omitempty"` // set when its firmware is older than the newest on the same model
	// NewestFrom says where Newest comes from: "controller" (the device's controller offers it) or
	// "fleet" (another device of the same model runs it).
	NewestFrom string `json:"newest_from,omitempty"`
	deviceFacts
}

// GET /api/inventory?probe=&group=: every device the user sees, with its facts, grouped by class and
// marked when its firmware is behind the newest on the same model.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	idx, err := s.hostIndex(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	vis, err := s.visibleHosts(ctx, scopeFrom(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	filter := parseHostFilter(r)
	facts, err := s.collectFacts(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	classes, _ := s.st.DeviceClasses(ctx)
	own, _ := s.st.HostFacts(ctx)
	names := s.proxyNames(ctx)
	rows := []inventoryRow{}
	for id, h := range idx {
		if (vis != nil && !vis[id]) || (filter.active() && !filter.matches(h)) {
			continue
		}
		row := inventoryRow{HostID: id, Name: h.Name, Groups: h.Groups, Probe: names[h.ProxyID], ClassID: classes[id],
			AssetTag: own[id].AssetTag, Location: own[id].Location}
		if row.Groups == nil {
			row.Groups = []string{}
		}
		if c, ok := provision.ClassByID(row.ClassID); ok {
			if c.Internal {
				continue // Argus's own probe hosts aren't devices
			}
			row.Class = c.Label
		}
		if f := facts[id]; f != nil {
			row.deviceFacts = *f
		}
		rows = append(rows, row)
	}
	markOlderFirmware(rows)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Class != rows[j].Class {
			return rows[i].Class < rows[j].Class
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	writeJSON(w, http.StatusOK, rows)
}

// markOlderFirmware sets Newest on each device behind on firmware. A device whose controller says
// (UniFi) has its word for it: models run different firmware lines (the 2.5G switches 2.x, the others
// 7.x), which only the controller knows. Otherwise a device is compared with the others of exactly its
// model, on firmware or, without one, its OS; a device whose model isn't known isn't compared at all.
func markOlderFirmware(rows []inventoryRow) {
	ver := func(r inventoryRow) string {
		if r.Firmware != "" && !strings.Contains(r.Firmware, ",") {
			return r.Firmware
		}
		return r.OS
	}
	newest := map[string]string{}
	for _, r := range rows {
		v := ver(r)
		if v == "" || r.Model == "" || r.upgradeKnown {
			continue
		}
		if cur, ok := newest[r.Model]; !ok {
			newest[r.Model] = v
		} else if less, ok := fwOlder(cur, v); ok && less {
			newest[r.Model] = v
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.upgradeKnown {
			if r.Upgrade != "" {
				r.Newest, r.NewestFrom = r.Upgrade, "controller"
			}
			continue
		}
		v := ver(*r)
		if n := newest[r.Model]; r.Model != "" && v != "" && n != "" && n != v {
			if less, ok := fwOlder(v, n); ok && less {
				r.Newest, r.NewestFrom = n, "fleet"
			}
		}
	}
}

// --- the journal ---

type journalView struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	By     string `json:"by"`
	At     int64  `json:"at"`
	Mine   bool   `json:"mine,omitempty"`
	CanDel bool   `json:"can_delete,omitempty"`
}

const journalMax = 1000

func journalViewOf(e store.JournalEntry, u *store.User) journalView {
	v := journalView{ID: e.ID, Kind: e.Kind, Text: e.Text, By: e.ByName, At: e.CreatedAt}
	if u != nil {
		v.Mine = e.ByUser == u.ID
		v.CanDel = u.Role == "admin" || (v.Mine && u.Role == "helpdesk")
	}
	return v
}

// GET /api/hosts/{id}/journal: a host's journal, newest first.
func (s *Server) handleHostJournal(w http.ResponseWriter, r *http.Request) {
	es, err := s.st.HostJournal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the journal"})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	out := make([]journalView, 0, len(es))
	for _, e := range es {
		out = append(out, journalViewOf(e, u))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/hosts/{id}/journal (admin, helpdesk): add an entry.
func (s *Server) handleAddJournal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || utf8.RuneCountInString(text) > journalMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an entry is 1 to 1000 characters"})
		return
	}
	kind := req.Kind
	if kind != "warning" && kind != "problem" {
		kind = "info"
	}
	u, _ := auth.UserFrom(r.Context())
	e := store.JournalEntry{HostID: r.PathValue("id"), Kind: kind, Text: text}
	if u != nil {
		e.ByUser, e.ByName = u.ID, userLabel(u)
	}
	e, err := s.st.AddJournalEntry(r.Context(), e)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the entry"})
		return
	}
	writeJSON(w, http.StatusOK, journalViewOf(e, u))
}

// DELETE /api/journal/{id}: an admin removes any entry, helpdesk their own.
func (s *Server) handleDeleteJournal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such entry"})
		return
	}
	e, err := s.st.JournalEntryByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such entry"})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	if !s.hostInScope(r.Context(), scopeFrom(r), e.HostID) || u == nil || !journalViewOf(e, u).CanDel {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can remove only your own entries"})
		return
	}
	if err := s.st.DeleteJournalEntry(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove the entry"})
		return
	}
	if idx, err := s.hostIndex(r.Context()); err == nil {
		changeObject(r, idx[e.HostID].Name, e.HostID)
	}
	changeDetail(r, e.Text)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- link templates (Settings, Device links) ---

type linkTemplateView struct {
	ID      int64    `json:"id"`
	Label   string   `json:"label"`
	URL     string   `json:"url"`
	Classes []string `json:"classes"`
}

// GET /api/links (admin): the link templates.
func (s *Server) handleLinkTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.LinkTemplates(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the links"})
		return
	}
	out := make([]linkTemplateView, 0, len(ts))
	for _, t := range ts {
		out = append(out, linkTemplateView{ID: t.ID, Label: t.Label, URL: t.URL, Classes: nonNilStrings(t.Classes)})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/links, PATCH /api/links/{id} (admin): save a link template.
func (s *Server) handleSaveLinkTemplate(w http.ResponseWriter, r *http.Request) {
	var req linkTemplateView
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Label, req.URL = strings.TrimSpace(req.Label), strings.TrimSpace(req.URL)
	if req.Label == "" || utf8.RuneCountInString(req.Label) > 40 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a label is 1 to 40 characters"})
		return
	}
	if msg := checkLinkTemplate(req.URL); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	for _, c := range req.Classes {
		if _, ok := provision.ClassByID(c); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown device class " + c})
			return
		}
	}
	t := store.LinkTemplate{Label: req.Label, URL: req.URL, Classes: req.Classes}
	if v := r.PathValue("id"); v != "" {
		t.ID, _ = strconv.ParseInt(v, 10, 64)
	}
	id, err := s.st.SaveLinkTemplate(r.Context(), t)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such link"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the link"})
		return
	}
	changeObject(r, req.Label)
	changeDetail(r, req.URL)
	writeJSON(w, http.StatusOK, linkTemplateView{ID: id, Label: req.Label, URL: req.URL, Classes: nonNilStrings(req.Classes)})
}

// DELETE /api/links/{id} (admin).
func (s *Server) handleDeleteLinkTemplate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if ts, err := s.st.LinkTemplates(r.Context()); err == nil {
		for _, t := range ts {
			if t.ID == id {
				changeObject(r, t.Label)
			}
		}
	}
	if err := s.st.DeleteLinkTemplate(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the link"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// hostOwnLinks validates a host's own links as the settings editor sends them.
func hostOwnLinks(in []linkView) ([]store.Link, string) {
	var out []store.Link
	for _, l := range in {
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		if l.Label == "" && l.URL == "" {
			continue
		}
		if l.Label == "" || utf8.RuneCountInString(l.Label) > 40 {
			return nil, "a link's label is 1 to 40 characters"
		}
		if msg := checkLinkTemplate(l.URL); msg != "" {
			return nil, l.Label + ": " + msg
		}
		out = append(out, store.Link{Label: l.Label, URL: l.URL})
	}
	if len(out) > 20 {
		return nil, "at most 20 links per host"
	}
	return out, ""
}
