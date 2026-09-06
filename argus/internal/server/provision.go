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
	}()
}

type classView struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Family     string `json:"family"`
	Pattern    string `json:"pattern"`
	Iface      string `json:"iface"`
	OffersHTTP bool   `json:"offers_http"`
}

// GET /api/classes - the device-class catalog for the attach UI (any signed-in user).
func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	out := make([]classView, 0)
	for _, c := range provision.Classes() {
		out = append(out, classView{ID: c.ID, Label: c.Label, Family: c.Family, Pattern: string(c.Pattern), Iface: string(c.Iface), OffersHTTP: c.OffersHTTP})
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
	if class.Iface == provision.IfaceSNMP && req.SNMP == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this class needs SNMP settings"})
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

	hostID, err := s.zbx.CreateHost(ctx, zabbix.CreateHostParams{
		Host:        req.Name,
		Name:        req.Visible,
		GroupIDs:    []string{groupID},
		TemplateIDs: tmplIDs,
		Interfaces:  buildInterface(class, req, useIP),
		Macros:      buildMacros(req),
		MonitoredBy: monitoredBy,
		ProxyID:     proxyID,
		Tags: []zabbix.HostTag{
			{Tag: "argus.class", Value: class.ID},
			{Tag: "argus.source", Value: "manual"},
		},
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	// Record the Argus overlay. A failure here doesn't undo the host (it exists + is monitored); it
	// just means the class tag on the Zabbix host is the only record until the next reconcile.
	if err := s.st.SetDeviceClass(ctx, hostID, class.ID, "manual"); err != nil {
		s.logger.Error("provision: could not record device-class overlay", "host", hostID, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": hostID, "class": class.ID})
}

// buildInterface builds the host's Zabbix interface for its class: SNMP (type 2) for SNMP classes,
// otherwise an agent interface (type 1) that ICMP/simple checks resolve their target from.
func buildInterface(class provision.Class, req createHostRequest, useIP bool) []zabbix.HostInterface {
	if class.Iface == provision.IfaceNone {
		return nil
	}
	u := 0
	if useIP {
		u = 1
	}
	if class.Iface == provision.IfaceSNMP {
		port := "161"
		var snmp *zabbix.SNMPDetails
		if req.SNMP != nil {
			if req.SNMP.Port != "" {
				port = req.SNMP.Port
			}
			v := req.SNMP.Version
			if v == 0 {
				v = 2
			}
			snmp = &zabbix.SNMPDetails{Version: v, Community: req.SNMP.Community, Bulk: 1}
		}
		return []zabbix.HostInterface{{Type: 2, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: port, SNMP: snmp}}
	}
	return []zabbix.HostInterface{{Type: 1, Main: 1, UseIP: u, IP: req.IP, DNS: req.DNS, Port: "10050"}}
}

// buildMacros turns the request's HTTP add-on port/scheme and any extra overrides into host macros.
// Host-level macros override the template defaults (the §6 thresholds live in the templates).
func buildMacros(req createHostRequest) []zabbix.Macro {
	var macros []zabbix.Macro
	if req.HTTP {
		if p := strings.TrimSpace(req.HTTPPort); p != "" {
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.PORT}", Value: p})
		}
		if sc := strings.TrimSpace(req.HTTPScheme); sc != "" {
			macros = append(macros, zabbix.Macro{Macro: "{$HTTP.SCHEME}", Value: sc})
		}
	}
	for k, v := range req.Macros {
		if k = strings.TrimSpace(k); k != "" {
			macros = append(macros, zabbix.Macro{Macro: k, Value: v})
		}
	}
	return macros
}
