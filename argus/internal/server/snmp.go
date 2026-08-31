package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// defaultToView renders a stored SNMP default for the browser, masking the v3 passphrases.
func defaultToView(d store.SNMPDefault) *snmpView {
	return &snmpView{
		Version: d.Version, Community: d.Community, Bulk: d.Bulk,
		SecurityName: d.SecName, SecurityLevel: d.SecLevel,
		AuthProtocol: d.AuthProto, AuthPassphrase: "",
		PrivProtocol: d.PrivProto, PrivPassphrase: "",
		ContextName: d.ContextName,
	}
}

// viewToDefault maps the browser shape to a stored default, carrying forward masked (blank) v3
// passphrases from the current default so they aren't wiped.
func viewToDefault(v *snmpView, cur store.SNMPDefault) store.SNMPDefault {
	d := store.SNMPDefault{
		Version: v.Version, Community: v.Community, Bulk: v.Bulk,
		SecName: v.SecurityName, SecLevel: v.SecurityLevel,
		AuthProto: v.AuthProtocol, AuthPass: v.AuthPassphrase,
		PrivProto: v.PrivProtocol, PrivPass: v.PrivPassphrase,
		ContextName: v.ContextName,
	}
	if d.AuthPass == "" {
		d.AuthPass = cur.AuthPass
	}
	if d.PrivPass == "" {
		d.PrivPass = cur.PrivPass
	}
	return d
}

// defaultToDetails converts a stored default to a Zabbix SNMP interface detail (effective values).
func defaultToDetails(d store.SNMPDefault) *zabbix.SNMPDetails {
	return &zabbix.SNMPDetails{
		Version: d.Version, Community: d.Community, Bulk: d.Bulk,
		SecurityName: d.SecName, SecurityLevel: d.SecLevel,
		AuthProtocol: d.AuthProto, AuthPassphrase: d.AuthPass,
		PrivProtocol: d.PrivProto, PrivPassphrase: d.PrivPass,
		ContextName: d.ContextName,
	}
}

// handleGetProxySNMP returns a proxy's SNMP default (v3 passphrases masked), and whether one is set.
func (s *Server) handleGetProxySNMP(w http.ResponseWriter, r *http.Request) {
	d, ok, err := s.st.SNMPDefaultFor(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the SNMP default"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"set": false, "snmp": &snmpView{Version: 2, Community: "public", Bulk: 1}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"set": true, "snmp": defaultToView(d)})
}

// handleSetProxySNMP saves a proxy's SNMP default and propagates it to every inheriting host interface
// on that proxy. Admin/helpdesk.
func (s *Server) handleSetProxySNMP(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	proxyID := r.PathValue("id")
	var v snmpView
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if v.Version < 1 || v.Version > 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an SNMP version (1, 2 or 3) is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	cur, _, _ := s.st.SNMPDefaultFor(ctx, proxyID)
	def := viewToDefault(&v, cur)
	if err := s.st.SetSNMPDefault(ctx, proxyID, def); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the SNMP default"})
		return
	}

	// Propagate to inheriting interfaces on this proxy's hosts (best-effort per interface).
	inherit, _ := s.st.SNMPInheritMap(ctx)
	hosts, err := s.zbx.HostsByProxy(ctx, proxyID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "updated": 0, "warning": "saved, but could not list the proxy's hosts to propagate: " + err.Error()})
		return
	}
	details := defaultToDetails(def)
	updated, failed, overrides := 0, 0, 0
	for _, h := range hosts {
		for _, i := range h.Interfaces {
			if i.Type != 2 {
				continue
			}
			if !inherit[i.InterfaceID] {
				overrides++ // an SNMP interface with its own creds - a candidate to adopt this default
				continue
			}
			i.SNMP = details
			if err := s.zbx.UpdateHostInterface(ctx, i); err != nil {
				s.logger.Warn("snmp propagate: interface update failed", "interface", i.InterfaceID, "err", err)
				failed++
				continue
			}
			updated++
		}
	}
	resp := map[string]any{"status": "ok", "updated": updated, "overrides": overrides}
	if failed > 0 {
		resp["warning"] = "some interfaces could not be updated"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdoptProxySNMP switches every override SNMP interface on a proxy's hosts to inherit the proxy
// default (offered after a default is first set). Admin/helpdesk.
func (s *Server) handleAdoptProxySNMP(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	proxyID := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	def, ok, err := s.st.SNMPDefaultFor(ctx, proxyID)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set an SNMP default for this proxy first"})
		return
	}
	inherit, _ := s.st.SNMPInheritMap(ctx)
	hosts, err := s.zbx.HostsByProxy(ctx, proxyID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	details := defaultToDetails(def)
	flipped := 0
	for _, h := range hosts {
		for _, i := range h.Interfaces {
			if i.Type != 2 || inherit[i.InterfaceID] {
				continue // already inheriting - leave it
			}
			i.SNMP = details
			if err := s.zbx.UpdateHostInterface(ctx, i); err != nil {
				s.logger.Warn("snmp adopt: interface update failed", "interface", i.InterfaceID, "err", err)
				continue
			}
			_ = s.st.SetSNMPInherit(ctx, i.InterfaceID, true)
			flipped++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "adopted": flipped})
}
