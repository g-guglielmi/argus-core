// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// This file is the C0 manual-attach seam (ROADMAP §C): import the class templates at startup and
// let an admin create a host from a device class. It's the minimal path the discovery pipeline (§B)
// and the management UI (§D) build on.

// startTemplateReconcile imports the device-class templates into Zabbix in the background at startup.
// Idempotent (imports only when a template file changed) and soft-skips when no Zabbix token is set
// yet; the create path re-checks before it needs them, so a token configured later still works. An
// import that fails is kept (Updates shows it, with Zabbix's reason) and tried again every 15
// minutes until it goes through, so new sensors never wait silently for a restart.
func (s *Server) startTemplateReconcile(ctx context.Context) {
	go func() {
		s.reconcileTemplates(ctx)
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if failed, _, _ := s.templatesFailure(ctx); failed != "" {
					s.reconcileTemplates(ctx)
				}
			}
		}
	}()
}

const (
	metaTemplatesError   = "templates_import_error"
	metaTemplatesErrorAt = "templates_import_error_at"
)

// reconcileTemplates imports the templates, records how it went, then re-applies the fleet-wide
// threshold defaults and makes sure every probe has its Probe host.
func (s *Server) reconcileTemplates(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := provision.Reconcile(c, s.zbx, s.st, s.logger); err != nil {
		s.logger.Error("provision: template reconcile failed (retried every 15 minutes)", "err", err)
		_ = s.st.MetaSet(c, metaTemplatesError, err.Error())
		_ = s.st.MetaSet(c, metaTemplatesErrorAt, fmt.Sprint(time.Now().Unix()))
	} else {
		_ = s.st.MetaDelete(c, metaTemplatesError)
		_ = s.st.MetaDelete(c, metaTemplatesErrorAt)
	}
	// Overlay the admin's stored fleet-wide threshold defaults onto the (possibly re-imported)
	// templates, so a template re-import can't silently reset them to factory values (§D).
	if err := provision.ApplyGlobalThresholds(c, s.zbx, s.st, s.logger); err != nil {
		s.logger.Error("thresholds: applying global defaults failed (will retry on next restart/save)", "err", err)
	}
	// Every probe gets its Argus-managed Probe health host (needs the template imported above).
	s.EnsureProbeHosts(c)
	// The server's own host becomes the Argus core (needs the template imported above).
	s.EnsureCoreHost(c)
}

// templatesFailure is the last template import's failure ("" when it went through), and when.
func (s *Server) templatesFailure(ctx context.Context) (string, int64, error) {
	msg, ok, err := s.st.MetaGet(ctx, metaTemplatesError)
	if err != nil || !ok {
		return "", 0, err
	}
	at, _, _ := s.st.MetaGet(ctx, metaTemplatesErrorAt)
	return msg, atoi64(at), nil
}

type classView struct {
	ID         string                `json:"id"`
	Label      string                `json:"label"`
	Family     string                `json:"family"`
	Pattern    string                `json:"pattern"`
	Iface      string                `json:"iface"`
	Sets       []string              `json:"sets"` // the sensor sets a device of this class usually has
	OffersHTTP bool                  `json:"offers_http"`
	Icon       string                `json:"icon"`
	Macros     []provision.MacroSpec `json:"macros,omitempty"`   // per-host inputs the attach form collects
	Setup      *provision.ClassSetup `json:"setup,omitempty"`    // prerequisite steps shown in the attach form
	Internal   bool                  `json:"internal,omitempty"` // Argus-managed (Probe host): kept for label lookups, never offered in pickers
}

// GET /api/classes - the device-class catalog for the attach UI (any signed-in user).
func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	out := make([]classView, 0)
	for _, c := range provision.Classes() {
		out = append(out, classView{ID: c.ID, Label: c.Label, Family: c.Family, Pattern: string(c.Pattern), Iface: string(c.Iface), Sets: append([]string{}, c.Sets...), OffersHTTP: c.OffersHTTP, Icon: c.Icon, Macros: c.Macros, Setup: c.Setup, Internal: c.Internal})
	}
	writeJSON(w, http.StatusOK, out)
}

// setView is a sensor set as the browser reads it: the catalog entry plus its slot's rule in words.
type setView struct {
	provision.Set
	SlotNote string `json:"slot_note,omitempty"`
}

type setCatalogView struct {
	Groups []provision.SetGroup `json:"groups"`
	Sets   []setView            `json:"sets"`
}

func setCatalog() setCatalogView {
	out := setCatalogView{Groups: provision.SetGroups(), Sets: []setView{}}
	for _, st := range provision.Sets() {
		out.Sets = append(out.Sets, setView{Set: st, SlotNote: provision.SlotNote(st.Slot)})
	}
	return out
}

// GET /api/sets - the sensor-set catalog (DESIGN 5b): every block of sensors a host can carry, with
// the settings its form collects and the rules it follows (any signed-in user).
func (s *Server) handleSets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, setCatalog())
}

type snmpReq struct {
	Version   int    `json:"version"` // 1, 2 (v2c), 3
	Community string `json:"community"`
	Port      string `json:"port"` // default 161
}

type createHostRequest struct {
	Name    string `json:"name"`         // technical name (unique), also the default visible name
	Visible string `json:"visible_name"` // optional visible-name override
	IP      string `json:"ip"`
	DNS     string `json:"dns"`
	UseIP   *bool  `json:"use_ip"` // default: true when an IP is given, else false
	Site    string `json:"site"`   // host group name
	ProxyID string `json:"proxy_id"`
	ClassID string `json:"class_id"`
	// Sets lists the sensor sets the host carries (ids, DESIGN 5b). Nil = its class's usual ones,
	// what discovery, an import and the API's older callers send.
	Sets       *[]string         `json:"sets"`
	HTTP       bool              `json:"http"`        // also carry the HTTP/HTTPS set (the older way to ask)
	HTTPPort   string            `json:"http_port"`   // custom port for it (macro override)
	HTTPScheme string            `json:"http_scheme"` // http | https (macro override)
	SNMP       *snmpReq          `json:"snmp"`        // SNMP credentials for this host, instead of its collector's default
	Macros     map[string]string `json:"macros"`      // extra per-host macro overrides
	// Set when the host is adopted from the Discovery review screen (§B): the discovery result this
	// host came from. The host is tagged/recorded with source "discovered" and the result is marked
	// added, so a re-scan shows it as already monitored.
	DiscoveryResultID int64 `json:"discovery_result_id,omitempty"`
}

// POST /api/hosts - create a monitored host from a device class. Admin only (wired in server.go).
func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req createHostRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	// A sweep-adopted UniFi device gets its controller macros (URL/KEY/MAC/SITE) filled from the
	// saved controller server-side, BEFORE the required-macro check - the API key never travels
	// through the browser. See injectUniFiMacros in netdiscovery.go.
	if class, ok := provision.ClassByID(req.ClassID); ok && req.DiscoveryResultID > 0 && strings.HasPrefix(class.ID, "unifi-") {
		s.injectUniFiMacros(r.Context(), &req)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	source := "manual"
	if req.DiscoveryResultID > 0 {
		source = "discovered"
	}
	hostID, perr := s.provisionHost(ctx, &req, source, false)
	if perr != nil {
		writeJSON(w, perr.status, map[string]string{"error": perr.msg})
		return
	}
	class, _ := provision.ClassByID(req.ClassID)
	// Adopted from a discovery scan: mark the result, so the review screen and future re-scans show
	// it as monitored. Best-effort - the host itself is already created.
	if req.DiscoveryResultID > 0 {
		if err := s.st.MarkDiscoveryResultAdded(ctx, req.DiscoveryResultID, hostID); err != nil {
			s.logger.Warn("provision: could not mark discovery result adopted", "result", req.DiscoveryResultID, "err", err)
		}
	}
	shown := req.Name
	if strings.TrimSpace(req.Visible) != "" {
		shown = req.Visible
	}
	changeObject(r, shown, hostID)
	how := "by hand"
	if req.DiscoveryResultID > 0 {
		how = "adopted from discovery"
	}
	changeDetail(r, class.Label+" in "+req.Site+", "+how)
	writeJSON(w, http.StatusOK, map[string]string{"id": hostID, "class": class.ID})
}

// provisionError is why a host couldn't be created, with the HTTP status that says so.
type provisionError struct {
	status int
	msg    string
}

func provisionFail(status int, msg string) *provisionError {
	return &provisionError{status: status, msg: msg}
}

// provisionHost creates a monitored host from a device class: the single path a host is made by, by
// hand, from discovery or from an import (source: manual | discovered | imported). It cleans req in
// place (names, addresses). reconciled skips the template check when the caller already made it (an
// import runs it once for all its hosts).
func (s *Server) provisionHost(ctx context.Context, req *createHostRequest, source string, reconciled bool) (string, *provisionError) {
	req.Name, req.Site = strings.TrimSpace(req.Name), strings.TrimSpace(req.Site)
	req.IP, req.DNS = strings.TrimSpace(req.IP), strings.TrimSpace(req.DNS)

	class, ok := provision.ClassByID(req.ClassID)
	if !ok || class.Internal { // Argus-managed classes (the Probe host) are never created by hand
		return "", provisionFail(http.StatusBadRequest, "unknown device class")
	}
	if req.Name == "" {
		return "", provisionFail(http.StatusBadRequest, "a host name is required")
	}
	// Zabbix's technical name takes only letters, digits, spaces, dots, dashes and underscores; the
	// visible name takes anything. A name like "U6+ Salotto" keeps that as its visible name and gets a
	// technical name Zabbix accepts ("U6_ Salotto").
	if tech := zabbix.TechnicalName(req.Name); tech != req.Name {
		if strings.TrimSpace(req.Visible) == "" {
			req.Visible = req.Name
		}
		req.Name = tech
	}
	if req.Site == "" {
		return "", provisionFail(http.StatusBadRequest, "a site (host group) is required")
	}
	if req.IP == "" && req.DNS == "" {
		return "", provisionFail(http.StatusBadRequest, "an IP address or DNS name is required")
	}
	// Its sensor sets, and the settings they can't do without (an API endpoint, credentials).
	hostSets, serr := hostSetsFor(*req, class)
	if serr != nil {
		return "", provisionFail(http.StatusBadRequest, serr.Error())
	}
	for _, st := range hostSets {
		for _, ms := range st.Macros {
			if ms.Required && strings.TrimSpace(req.Macros[ms.Macro]) == "" {
				return "", provisionFail(http.StatusBadRequest, ms.Label+" is required for "+st.Label)
			}
		}
	}
	useIP := req.IP != ""
	if req.UseIP != nil {
		useIP = *req.UseIP
	}
	if useIP && req.IP == "" {
		return "", provisionFail(http.StatusBadRequest, "connect-by-IP needs an IP address")
	}
	if !useIP && req.DNS == "" {
		return "", provisionFail(http.StatusBadRequest, "connect-by-DNS needs a DNS name")
	}

	// Ensure the class templates are present (idempotent; imports only when changed). A template Zabbix
	// refuses doesn't stop the host: it gets the templates Zabbix has (a class whose own template was
	// never imported fails below, by name), and Updates shows the failure.
	if !reconciled {
		if err := provision.Reconcile(ctx, s.zbx, s.st, s.logger); err != nil {
			s.logger.Warn("provision: creating the host with the templates Zabbix has", "err", err)
		}
	}

	// Reject a duplicate technical name up front with a clean message.
	if existing, err := s.zbx.HostIDByName(ctx, req.Name); err == nil && existing != "" {
		return "", provisionFail(http.StatusConflict, "a host with this name already exists")
	}

	// Resolve templates: Base Ping (always) + each set's.
	names := []string{provision.TemplateBasePing}
	for _, st := range hostSets {
		names = append(names, st.Template)
	}
	ids, err := s.zbx.TemplateIDsByName(ctx, names)
	if err != nil {
		return "", provisionFail(http.StatusBadGateway, "Zabbix: "+zabbix.UserMessage(err))
	}
	tmplIDs := make([]string, 0, len(names))
	for _, n := range names {
		tmplIDs = append(tmplIDs, ids[n])
	}

	groupID, err := s.zbx.EnsureHostGroupID(ctx, req.Site)
	if err != nil {
		return "", provisionFail(http.StatusBadGateway, "Zabbix: "+zabbix.UserMessage(err))
	}

	monitoredBy, proxyID := 0, ""
	if req.ProxyID != "" && req.ProxyID != "0" {
		monitoredBy, proxyID = 1, req.ProxyID
	}

	// Build the interfaces its sets need. SNMP credentials inherit the proxy's SNMP default (like
	// the rest of Argus) unless the request carries an explicit override.
	ifaces, inheritSNMP, ifErr := s.resolveInterfaces(ctx, setIfaces(hostSets), *req, useIP, proxyID)
	if ifErr != "" {
		return "", provisionFail(http.StatusBadRequest, ifErr)
	}

	macros, err := buildMacros(*req, class)
	if err != nil {
		return "", provisionFail(http.StatusBadRequest, err.Error())
	}
	hostID, err := s.zbx.CreateHost(ctx, zabbix.CreateHostParams{
		Host:        req.Name,
		Name:        req.Visible,
		GroupIDs:    []string{groupID},
		TemplateIDs: tmplIDs,
		Interfaces:  ifaces,
		Macros:      macros,
		MonitoredBy: monitoredBy,
		ProxyID:     proxyID,
		Tags: []zabbix.HostTag{
			{Tag: "argus.class", Value: class.ID},
			{Tag: "argus.source", Value: source},
		},
	})
	if err != nil {
		return "", provisionFail(http.StatusBadGateway, "Zabbix: "+zabbix.UserMessage(err))
	}
	// Record the Argus overlay. A failure here doesn't undo the host (it exists + is monitored); it
	// just means the class tag on the Zabbix host is the only record until the next reconcile.
	if err := s.st.SetDeviceClass(ctx, hostID, class.ID, source); err != nil {
		s.logger.Error("provision: could not record device-class overlay", "host", hostID, "err", err)
	}
	// Mark the SNMP interface as inheriting its proxy default, so a later change to that default
	// propagates here like every other inheriting interface (server/snmp.go).
	if inheritSNMP {
		if hd, err := s.zbx.HostDetail(ctx, hostID); err == nil {
			for _, i := range hd.Interfaces {
				if i.Type == 2 {
					_ = s.st.SetSNMPInherit(ctx, i.InterfaceID, true)
				}
			}
		}
	}
	// Kick the class's discovery rules shortly after creation (delayed until the proxy has synced the
	// new config), so per-instance sensors appear in seconds instead of after the rules' interval.
	s.scheduleDiscovery(hostID)
	s.forgetHostIndex()
	return hostID, nil
}

// ensureIface adds the interface a sensor set needs when the host lacks it (SNMP credentials inherit
// the collector's default, or come from snmp). who names the set in the error. Returns a user-facing
// error, or "".
func (s *Server) ensureIface(ctx context.Context, hd *zabbix.HostDetail, kind provision.IfaceKind, who string, snmp *snmpReq) string {
	want := 1 // agent
	switch kind {
	case provision.IfaceNone:
		return ""
	case provision.IfaceSNMP:
		want = 2
	}
	// Reuse the host's existing main interface address as the connection target.
	var useIP, ip, dns string
	for _, i := range hd.Interfaces {
		if i.Type == want {
			return "" // already has the needed type
		}
		if useIP == "" && (i.IP != "" || i.DNS != "") {
			ip, dns = i.IP, i.DNS
			if i.UseIP == 1 {
				useIP = "ip"
			} else {
				useIP = "dns"
			}
		}
	}
	if useIP == "" {
		return who + " needs an address to read: give the host an interface first"
	}
	u := 1
	if useIP == "dns" {
		u = 0
	}
	if want == 1 {
		if _, err := s.zbx.CreateHostInterface(ctx, hd.HostID, zabbix.HostInterface{Type: 1, Main: 1, UseIP: u, IP: ip, DNS: dns, Port: "10050"}); err != nil {
			return "Zabbix: " + zabbix.UserMessage(err)
		}
		return ""
	}
	// SNMP: creds from the request override, else the collector's SNMP default.
	var details *zabbix.SNMPDetails
	inherit := false
	if snmp != nil {
		v := snmp.Version
		if v == 0 {
			v = 2
		}
		details = &zabbix.SNMPDetails{Version: v, Community: snmp.Community, Bulk: 1}
	} else {
		defID := "0"
		if hd.MonitoredBy == 1 && hd.ProxyID != "" && hd.ProxyID != "0" {
			defID = hd.ProxyID
		}
		if def, ok, _ := s.st.SNMPDefaultFor(ctx, defID); ok {
			details = defaultToDetails(def)
			inherit = true
		}
	}
	if details == nil {
		return who + " reads over SNMP, and this host has no SNMP interface: add one under Interfaces with its credentials, or set its collector's SNMP default (Probes, or Settings for the core)"
	}
	port := "161"
	if snmp != nil && snmp.Port != "" {
		port = snmp.Port
	}
	newID, err := s.zbx.CreateHostInterface(ctx, hd.HostID, zabbix.HostInterface{Type: 2, Main: 1, UseIP: u, IP: ip, DNS: dns, Port: port, SNMP: details})
	if err != nil {
		return "Zabbix: " + zabbix.UserMessage(err)
	}
	if inherit {
		_ = s.st.SetSNMPInherit(ctx, newID, true)
	}
	return ""
}

// hostSetsFor is the sensor sets a new host carries, in catalog order: its class's usual ones (with
// the HTTP check when the older flag asks for it), or exactly the ones the form listed.
func hostSetsFor(req createHostRequest, class provision.Class) ([]provision.Set, error) {
	ids := append([]string(nil), class.Sets...)
	if req.Sets != nil {
		ids = nil
		for _, id := range *req.Sets {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if req.HTTP && !slices.Contains(ids, "http") {
		ids = append(ids, "http")
	}
	if err := provision.CheckSets(ids, class.ID, class.Internal); err != nil {
		return nil, err
	}
	out := make([]provision.Set, 0, len(ids))
	for _, st := range provision.Sets() {
		if slices.Contains(ids, st.ID) {
			out = append(out, st)
		}
	}
	return out, nil
}

// setIfaces is the interfaces a host needs for its sets: one of each kind a set asks for, or the
// agent's alone when none asks (Ping and the checks a probe runs take whichever the host has).
func setIfaces(sets []provision.Set) []provision.IfaceKind {
	var out []provision.IfaceKind
	for _, k := range []provision.IfaceKind{provision.IfaceSNMP, provision.IfaceAgent} {
		for _, st := range sets {
			if st.Iface == k {
				out = append(out, k)
				break
			}
		}
	}
	if len(out) == 0 {
		out = []provision.IfaceKind{provision.IfaceAgent}
	}
	return out
}

// resolveInterfaces builds a new host's Zabbix interfaces, one per kind its sets need. An SNMP one
// inherits the selected proxy's SNMP default (defaultToDetails, like the rest of Argus) unless the
// request carries an explicit override; the bool reports whether to track it as inheriting. The
// string is a user-facing error (an SNMP host with neither a proxy default nor an override) or "".
func (s *Server) resolveInterfaces(ctx context.Context, kinds []provision.IfaceKind, req createHostRequest, useIP bool, proxyID string) ([]zabbix.HostInterface, bool, string) {
	u := 0
	if useIP {
		u = 1
	}
	var out []zabbix.HostInterface
	inherit := false
	for _, kind := range kinds {
		if kind != provision.IfaceSNMP {
			out = append(out, zabbix.HostInterface{Type: 1, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: "10050"})
			continue
		}
		port := "161"
		if req.SNMP != nil && req.SNMP.Port != "" {
			port = req.SNMP.Port
		}
		var details *zabbix.SNMPDetails
		if req.SNMP != nil { // explicit override entered in the form
			v := req.SNMP.Version
			if v == 0 {
				v = 2
			}
			details = &zabbix.SNMPDetails{Version: v, Community: req.SNMP.Community, Bulk: 1}
		} else {
			// The common case: inherit the collector's SNMP default - the proxy's, or the core
			// server's own default (stored under proxy id "0") for server-monitored hosts.
			defID := proxyID
			if defID == "" {
				defID = "0"
			}
			if def, ok, _ := s.st.SNMPDefaultFor(ctx, defID); ok {
				details = defaultToDetails(def)
				inherit = true
			}
		}
		if details == nil {
			return nil, false, "no SNMP settings: this host's collector has no SNMP default (set one in Probes - the probe's Defaults, or Core SNMP), or switch on the override and enter them here"
		}
		out = append(out, zabbix.HostInterface{Type: 2, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: port, SNMP: details})
	}
	return out, inherit, ""
}

// buildMacros turns the request's settings (those of its sensor sets, the HTTP check's port/scheme)
// into host macros. Host-level macros override the template defaults (the §6 thresholds live in the
// templates). Settings a set declares as secret (API keys) are stored as Zabbix secret macros -
// write-only afterwards.
func buildMacros(req createHostRequest, class provision.Class) ([]zabbix.Macro, error) {
	hostSets, err := hostSetsFor(req, class)
	if err != nil {
		return nil, err
	}
	// Only what its sets declare and the thresholds a host of its class may override can be set:
	// the curated model the settings editor enforces applies at creation too.
	specs := map[string]provision.MacroSpec{}
	secret := map[string]bool{}
	for _, st := range hostSets {
		for _, ms := range st.Macros {
			specs[ms.Macro] = ms
			if ms.Secret {
				secret[ms.Macro] = true
			}
		}
	}
	thresholds := map[string]bool{}
	for _, tt := range provision.ThresholdsForClass(class) {
		for _, sp := range tt.Specs {
			thresholds[sp.Macro] = true
		}
	}
	var macros []zabbix.Macro
	used := map[string]bool{}
	if req.HTTP {
		// No port: the scheme's own (the template's is blank, the collector picks 443 or 80).
		if p := strings.TrimSpace(req.HTTPPort); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("the HTTP port must be a number from 1 to 65535")
			}
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.PORT}", Value: p})
			used["{$HTTP.PORT}"] = true
		}
		if sc := strings.TrimSpace(req.HTTPScheme); sc != "" {
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.SCHEME}", Value: sc})
			used["{$HTTP.SCHEME}"] = true
		}
	}
	for k, v := range req.Macros {
		if k = strings.TrimSpace(k); k == "" || used[k] {
			continue // the HTTP check's port and scheme, already set above
		} else if strings.TrimSpace(v) == "" {
			continue // an empty optional field must not override the template default
		}
		v = strings.TrimSpace(v)
		switch ms, declared := specs[k]; {
		case declared:
			if err := provision.ValidateMacroValue(ms, v); err != nil {
				return nil, err
			}
		case thresholds[k]:
			if !provision.ValidThresholdValue(v) {
				return nil, fmt.Errorf("%s must be a plain number", k)
			}
		case k == "{$HTTP.PORT}" || k == "{$HTTP.SCHEME}":
		default:
			return nil, fmt.Errorf("%s is not a setting of this device's sensors", k)
		}
		m := zabbix.Macro{Macro: k, Value: v}
		if secret[k] {
			m.Type = 1
		}
		macros = append(macros, m)
		used[k] = true
	}
	// A set's presets (e.g. unRAID's filesystem skip list) fill in last - anything set above wins.
	for _, st := range hostSets {
		for _, pm := range st.HostMacros {
			if !used[pm.Macro] {
				macros = append(macros, zabbix.Macro{Macro: pm.Macro, Value: pm.Value})
				used[pm.Macro] = true
			}
		}
	}
	return macros, nil
}
