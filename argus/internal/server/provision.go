// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
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
// yet; the create path re-checks before it needs them, so a token configured later still works.
func (s *Server) startTemplateReconcile(ctx context.Context) {
	go func() {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := provision.Reconcile(c, s.zbx, s.st, s.logger); err != nil {
			s.logger.Error("provision: template reconcile failed (will retry on next host create/restart)", "err", err)
		}
		// Overlay the admin's stored fleet-wide threshold defaults onto the (possibly re-imported)
		// templates, so a template re-import can't silently reset them to factory values (§D).
		if err := provision.ApplyGlobalThresholds(c, s.zbx, s.st, s.logger); err != nil {
			s.logger.Error("thresholds: applying global defaults failed (will retry on next restart/save)", "err", err)
		}
	}()
}

type classView struct {
	ID         string                `json:"id"`
	Label      string                `json:"label"`
	Family     string                `json:"family"`
	Pattern    string                `json:"pattern"`
	Iface      string                `json:"iface"`
	OffersHTTP bool                  `json:"offers_http"`
	Icon       string                `json:"icon"`
	Macros     []provision.MacroSpec `json:"macros,omitempty"` // per-host inputs the attach form collects
	Setup      *provision.ClassSetup `json:"setup,omitempty"`  // prerequisite steps shown in the attach form
}

// GET /api/classes - the device-class catalog for the attach UI (any signed-in user).
func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	out := make([]classView, 0)
	for _, c := range provision.Classes() {
		out = append(out, classView{ID: c.ID, Label: c.Label, Family: c.Family, Pattern: string(c.Pattern), Iface: string(c.Iface), OffersHTTP: c.OffersHTTP, Icon: c.Icon, Macros: c.Macros, Setup: c.Setup})
	}
	writeJSON(w, http.StatusOK, out)
}

type snmpReq struct {
	Version   int    `json:"version"` // 1, 2 (v2c), 3
	Community string `json:"community"`
	Port      string `json:"port"` // default 161
}

type createHostRequest struct {
	Name       string            `json:"name"`         // technical name (unique), also the default visible name
	Visible    string            `json:"visible_name"` // optional visible-name override
	IP         string            `json:"ip"`
	DNS        string            `json:"dns"`
	UseIP      *bool             `json:"use_ip"` // default: true when an IP is given, else false
	Site       string            `json:"site"`   // host group name
	ProxyID    string            `json:"proxy_id"`
	ClassID    string            `json:"class_id"`
	HTTP       bool              `json:"http"`        // attach the HTTP/HTTPS add-on
	HTTPPort   string            `json:"http_port"`   // custom port for the add-on (macro override)
	HTTPScheme string            `json:"http_scheme"` // http | https (macro override)
	SNMP       *snmpReq          `json:"snmp"`        // required for SNMP-interface classes
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
	req.Name, req.Site = strings.TrimSpace(req.Name), strings.TrimSpace(req.Site)
	req.IP, req.DNS = strings.TrimSpace(req.IP), strings.TrimSpace(req.DNS)

	class, ok := provision.ClassByID(req.ClassID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown device class"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a host name is required"})
		return
	}
	if req.Site == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a site (host group) is required"})
		return
	}
	if req.IP == "" && req.DNS == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an IP address or DNS name is required"})
		return
	}
	// A sweep-adopted UniFi device gets its controller macros (URL/KEY/MAC/SITE) filled from the
	// saved controller server-side, BEFORE the required-macro check - the API key never travels
	// through the browser. See injectUniFiMacros in netdiscovery.go.
	if req.DiscoveryResultID > 0 && strings.HasPrefix(class.ID, "unifi-") {
		s.injectUniFiMacros(r.Context(), &req)
	}
	// Class-declared per-host macros (API endpoint, credentials, …) - the required ones must be set.
	for _, ms := range class.Macros {
		if ms.Required && strings.TrimSpace(req.Macros[ms.Macro]) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": ms.Label + " is required for this device class"})
			return
		}
	}
	useIP := req.IP != ""
	if req.UseIP != nil {
		useIP = *req.UseIP
	}
	if useIP && req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connect-by-IP needs an IP address"})
		return
	}
	if !useIP && req.DNS == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connect-by-DNS needs a DNS name"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Ensure the class templates are present (idempotent; imports only when changed).
	if err := provision.Reconcile(ctx, s.zbx, s.st, s.logger); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not import class templates: " + err.Error()})
		return
	}

	// Reject a duplicate technical name up front with a clean message.
	if existing, err := s.zbx.HostIDByName(ctx, req.Name); err == nil && existing != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a host with this name already exists"})
		return
	}

	// Resolve templates: Base Ping (always) + the class's templates + optional HTTP add-on.
	names := append([]string{provision.TemplateBasePing}, class.Templates...)
	if req.HTTP {
		names = append(names, provision.TemplateHTTP)
	}
	ids, err := s.zbx.TemplateIDsByName(ctx, names)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	tmplIDs := make([]string, 0, len(names))
	for _, n := range names {
		tmplIDs = append(tmplIDs, ids[n])
	}

	groupID, err := s.zbx.EnsureHostGroupID(ctx, req.Site)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}

	monitoredBy, proxyID := 0, ""
	if req.ProxyID != "" && req.ProxyID != "0" {
		monitoredBy, proxyID = 1, req.ProxyID
	}

	// Build the interface. For SNMP classes the credentials inherit the proxy's SNMP default (like
	// the rest of Argus) unless the request carries an explicit override.
	ifaces, inheritSNMP, ifErr := s.resolveInterface(ctx, class, req, useIP, proxyID)
	if ifErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ifErr})
		return
	}

	source := "manual"
	if req.DiscoveryResultID > 0 {
		source = "discovered"
	}
	hostID, err := s.zbx.CreateHost(ctx, zabbix.CreateHostParams{
		Host:        req.Name,
		Name:        req.Visible,
		GroupIDs:    []string{groupID},
		TemplateIDs: tmplIDs,
		Interfaces:  ifaces,
		Macros:      buildMacros(req, class),
		MonitoredBy: monitoredBy,
		ProxyID:     proxyID,
		Tags: []zabbix.HostTag{
			{Tag: "argus.class", Value: class.ID},
			{Tag: "argus.source", Value: source},
		},
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	// Record the Argus overlay. A failure here doesn't undo the host (it exists + is monitored); it
	// just means the class tag on the Zabbix host is the only record until the next reconcile.
	if err := s.st.SetDeviceClass(ctx, hostID, class.ID, source); err != nil {
		s.logger.Error("provision: could not record device-class overlay", "host", hostID, "err", err)
	}
	// Adopted from a discovery scan: mark the result, so the review screen and future re-scans show
	// it as monitored. Best-effort - the host itself is already created.
	if req.DiscoveryResultID > 0 {
		if err := s.st.MarkDiscoveryResultAdded(ctx, req.DiscoveryResultID, hostID); err != nil {
			s.logger.Warn("provision: could not mark discovery result adopted", "result", req.DiscoveryResultID, "err", err)
		}
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
	writeJSON(w, http.StatusOK, map[string]string{"id": hostID, "class": class.ID})
}

type changeClassRequest struct {
	ClassID string            `json:"class_id"`
	Macros  map[string]string `json:"macros"` // the new class's per-host + required macros
	SNMP    *snmpReq          `json:"snmp"`   // override for a new SNMP interface, when one must be added
}

// handleChangeHostClass switches an existing host to a different device class in place (no delete +
// recreate): it swaps the class's templates (keeping Base Ping + any add-ons), ensures the new class's
// interface type exists, applies the new class's preset + entered macros, and updates the overlay.
// History is kept for any template shared by the old and new class. Admin only (wired in server.go).
func (s *Server) handleChangeHostClass(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req changeClassRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	newClass, ok := provision.ClassByID(req.ClassID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown device class"})
		return
	}
	// The new class's required macros (credentials, endpoints) must be supplied, like Add-device.
	for _, ms := range newClass.Macros {
		if ms.Required && strings.TrimSpace(req.Macros[ms.Macro]) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": ms.Label + " is required for this device class"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := provision.Reconcile(ctx, s.zbx, s.st, s.logger); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not import class templates: " + err.Error()})
		return
	}

	hostID := r.PathValue("id")
	hd, err := s.zbx.HostDetail(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	oldClassID, _, _ := s.st.GetDeviceClass(ctx, hostID)
	oldClass, _ := provision.ClassByID(oldClassID) // zero value (no templates) if unknown/unset

	// Template diff: add the new class's templates that aren't linked yet; remove the old class's
	// templates that the new class doesn't also use (Base Ping + add-ons are never touched). Removing
	// clears that template's items/history - unavoidable when the monitoring changes.
	linkedNames, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	linked := map[string]bool{}
	for _, n := range linkedNames {
		linked[n] = true
	}
	newSet := map[string]bool{}
	for _, t := range newClass.Templates {
		newSet[t] = true
	}
	var toAdd, toRemove []string
	for _, t := range newClass.Templates {
		if !linked[t] {
			toAdd = append(toAdd, t)
		}
	}
	for _, t := range oldClass.Templates {
		if !newSet[t] && linked[t] {
			toRemove = append(toRemove, t)
		}
	}
	ids, err := s.zbx.TemplateIDsByName(ctx, append(append([]string{}, toAdd...), toRemove...))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	for _, t := range toAdd {
		if err := s.zbx.LinkHostTemplate(ctx, hostID, ids[t]); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
	}
	for _, t := range toRemove {
		if err := s.zbx.UnlinkHostTemplate(ctx, hostID, ids[t]); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
	}

	// Ensure the new class's interface type exists (e.g. agent-class host -> SNMP class needs an SNMP
	// interface); existing interfaces are left in place.
	if msg := s.ensureClassInterface(ctx, hd, newClass, req.SNMP); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	// Apply the new class's preset macros + the entered per-host macros.
	if err := s.applyNewClassMacros(ctx, hostID, newClass, req.Macros); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	if err := s.st.SetDeviceClass(ctx, hostID, newClass.ID, "manual"); err != nil {
		s.logger.Error("provision: could not record device-class overlay after class change", "host", hostID, "err", err)
	}
	// Keep the Zabbix-side argus.class tag aligned (preserving argus.source). The overlay above is
	// authoritative for Argus, so this is best-effort cosmetics for the raw Zabbix view.
	if err := s.zbx.SetHostTag(ctx, hostID, "argus.class", newClass.ID); err != nil {
		s.logger.Warn("provision: could not update argus.class tag after class change", "host", hostID, "err", err)
	}
	s.scheduleDiscovery(hostID)
	writeJSON(w, http.StatusOK, map[string]string{"id": hostID, "class": newClass.ID})
}

// ensureClassInterface adds the interface type a class needs when the host lacks it (SNMP creds inherit
// the collector default, or come from the request override). Returns a user-facing error, or "".
func (s *Server) ensureClassInterface(ctx context.Context, hd *zabbix.HostDetail, class provision.Class, snmp *snmpReq) string {
	want := 1 // agent
	switch class.Iface {
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
	u := 1
	if useIP == "dns" {
		u = 0
	}
	if want == 1 {
		if _, err := s.zbx.CreateHostInterface(ctx, hd.HostID, zabbix.HostInterface{Type: 1, Main: 1, UseIP: u, IP: ip, DNS: dns, Port: "10050"}); err != nil {
			return "Zabbix: " + err.Error()
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
		return "this class needs SNMP, but the host's collector has no SNMP default (set one in Probes, or switch on the SNMP override and enter the credentials)"
	}
	port := "161"
	if snmp != nil && snmp.Port != "" {
		port = snmp.Port
	}
	newID, err := s.zbx.CreateHostInterface(ctx, hd.HostID, zabbix.HostInterface{Type: 2, Main: 1, UseIP: u, IP: ip, DNS: dns, Port: port, SNMP: details})
	if err != nil {
		return "Zabbix: " + err.Error()
	}
	if inherit {
		_ = s.st.SetSNMPInherit(ctx, newID, true)
	}
	return ""
}

// applyNewClassMacros sets a class's preset macros and the entered per-host macros on a host (create or
// update; a blank entered value is left alone rather than deleted, since a class change is additive).
func (s *Server) applyNewClassMacros(ctx context.Context, hostID string, class provision.Class, entered map[string]string) error {
	cur := map[string]zabbix.HostMacro{}
	hm, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	for _, m := range hm {
		cur[m.Macro] = m
	}
	set := func(macro, value string, secret bool) error {
		mType := 0
		if secret {
			mType = 1
		}
		if existing, has := cur[macro]; has {
			if secret || existing.Value != value {
				return s.zbx.UpdateHostMacro(ctx, existing.MacroID, value, mType)
			}
			return nil
		}
		return s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: macro, Value: value, Type: mType})
	}
	for _, p := range class.HostMacros {
		if err := set(p.Macro, p.Value, false); err != nil {
			return err
		}
	}
	for _, ms := range class.Macros {
		v := strings.TrimSpace(entered[ms.Macro])
		if v == "" {
			continue // required ones were validated by the caller; blanks are left as-is
		}
		if err := set(ms.Macro, v, ms.Secret); err != nil {
			return err
		}
	}
	return nil
}

// resolveInterface builds the host's Zabbix interface for its class. For SNMP classes the credentials
// inherit the selected proxy's SNMP default (defaultToDetails, like the rest of Argus) unless the
// request carries an explicit override; the bool reports whether to track the interface as inheriting.
// The string is a user-facing error (an SNMP host with neither a proxy default nor an override) or "".
func (s *Server) resolveInterface(ctx context.Context, class provision.Class, req createHostRequest, useIP bool, proxyID string) ([]zabbix.HostInterface, bool, string) {
	if class.Iface == provision.IfaceNone {
		return nil, false, ""
	}
	u := 0
	if useIP {
		u = 1
	}
	if class.Iface != provision.IfaceSNMP {
		return []zabbix.HostInterface{{Type: 1, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: "10050"}}, false, ""
	}
	port := "161"
	if req.SNMP != nil && req.SNMP.Port != "" {
		port = req.SNMP.Port
	}
	var details *zabbix.SNMPDetails
	inherit := false
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
	return []zabbix.HostInterface{{Type: 2, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: port, SNMP: details}}, inherit, ""
}

// buildMacros turns the request's HTTP add-on port/scheme and any extra overrides into host macros.
// Host-level macros override the template defaults (the §6 thresholds live in the templates). Macros
// the class declares as secret (API keys) are stored as Zabbix secret macros - write-only afterwards.
func buildMacros(req createHostRequest, class provision.Class) []zabbix.Macro {
	secret := map[string]bool{}
	for _, ms := range class.Macros {
		if ms.Secret {
			secret[ms.Macro] = true
		}
	}
	var macros []zabbix.Macro
	used := map[string]bool{}
	if req.HTTP {
		if p := strings.TrimSpace(req.HTTPPort); p != "" {
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.PORT}", Value: p})
			used["{$HTTP.PORT}"] = true
		}
		if sc := strings.TrimSpace(req.HTTPScheme); sc != "" {
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.SCHEME}", Value: sc})
			used["{$HTTP.SCHEME}"] = true
		}
	}
	for k, v := range req.Macros {
		if k = strings.TrimSpace(k); k == "" {
			continue
		} else if strings.TrimSpace(v) == "" {
			continue // an empty optional field must not override the template default
		}
		m := zabbix.Macro{Macro: k, Value: v}
		if secret[k] {
			m.Type = 1
		}
		macros = append(macros, m)
		used[k] = true
	}
	// Class presets (e.g. unRAID's filesystem skip list) fill in last - anything set above wins.
	for _, pm := range class.HostMacros {
		if !used[pm.Macro] {
			macros = append(macros, zabbix.Macro{Macro: pm.Macro, Value: pm.Value})
		}
	}
	return macros
}
