package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// snmpView is an SNMP interface's credentials as sent to/from the browser. v3 passphrases are masked
// on read (returned blank) and only written when the client sends a non-empty value.
type snmpView struct {
	Version        int    `json:"version"`
	Community      string `json:"community"`
	Bulk           int    `json:"bulk"`
	SecurityName   string `json:"security_name"`
	SecurityLevel  int    `json:"security_level"`
	AuthProtocol   int    `json:"auth_protocol"`
	AuthPassphrase string `json:"auth_passphrase"`
	PrivProtocol   int    `json:"priv_protocol"`
	PrivPassphrase string `json:"priv_passphrase"`
	ContextName    string `json:"context_name"`
}

type ifaceView struct {
	InterfaceID string    `json:"interfaceid,omitempty"`
	Type        int       `json:"type"`
	UseIP       int       `json:"useip"`
	IP          string    `json:"ip"`
	DNS         string    `json:"dns"`
	Port        string    `json:"port"`
	SNMP        *snmpView `json:"snmp,omitempty"`
	Inherit     bool      `json:"inherit"` // SNMP interface: creds managed by the proxy default
}

type hostConfigView struct {
	HostID       string      `json:"hostid"`
	Host         string      `json:"host"`         // technical name
	Name         string      `json:"name"`         // visible name
	MonitoredBy  int         `json:"monitored_by"` // 0 server, 1 proxy, 2 proxy group
	ProxyID      string      `json:"proxy_id,omitempty"`
	ProxyName    string      `json:"proxy_name,omitempty"`
	ProxyDefault *snmpView   `json:"proxy_default,omitempty"` // the host's proxy SNMP default (masked), if set
	Interfaces   []ifaceView `json:"interfaces"`
}

// snmpToView converts client SNMP details to the browser shape, masking v3 passphrases.
func snmpToView(s *zabbix.SNMPDetails) *snmpView {
	if s == nil {
		return nil
	}
	return &snmpView{
		Version: s.Version, Community: s.Community, Bulk: s.Bulk,
		SecurityName: s.SecurityName, SecurityLevel: s.SecurityLevel,
		AuthProtocol: s.AuthProtocol, AuthPassphrase: "", // masked
		PrivProtocol: s.PrivProtocol, PrivPassphrase: "", // masked
		ContextName: s.ContextName,
	}
}

// handleHostConfig returns a host's identity + interfaces for the settings editor (read-only, any user).
func (s *Server) handleHostConfig(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	hd, err := s.zbx.HostDetail(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	out := hostConfigView{HostID: hd.HostID, Host: hd.Host, Name: hd.Name, MonitoredBy: hd.MonitoredBy, ProxyID: hd.ProxyID, Interfaces: make([]ifaceView, 0, len(hd.Interfaces))}
	if hd.ProxyID != "" && hd.ProxyID != "0" {
		if proxies, perr := s.zbx.Proxies(ctx); perr == nil {
			for _, p := range proxies {
				if p.ProxyID == hd.ProxyID {
					out.ProxyName = p.Name
					break
				}
			}
		}
		if def, ok, _ := s.st.SNMPDefaultFor(ctx, hd.ProxyID); ok {
			out.ProxyDefault = defaultToView(def)
		}
	}
	inherit, _ := s.st.SNMPInheritMap(ctx)
	for _, i := range hd.Interfaces {
		out.Interfaces = append(out.Interfaces, ifaceView{InterfaceID: i.InterfaceID, Type: i.Type, UseIP: i.UseIP, IP: i.IP, DNS: i.DNS, Port: i.Port, SNMP: snmpToView(i.SNMP), Inherit: i.Type == 2 && inherit[i.InterfaceID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateHostConfig reconciles a host's whole desired identity + interface set (admin/helpdesk).
func (s *Server) handleUpdateHostConfig(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Host        string      `json:"host"`
		Name        string      `json:"name"`
		MonitoredBy int         `json:"monitored_by"`
		ProxyID     string      `json:"proxy_id"`
		Interfaces  []ifaceView `json:"interfaces"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.Host = strings.TrimSpace(req.Host)
	req.Name = strings.TrimSpace(req.Name)
	if req.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the technical host name is required"})
		return
	}
	if req.MonitoredBy == 1 && strings.TrimSpace(req.ProxyID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a proxy is required when monitored by a proxy"})
		return
	}
	for _, i := range req.Interfaces {
		if i.Type < 1 || i.Type > 4 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid interface type"})
			return
		}
		if i.UseIP == 1 && strings.TrimSpace(i.IP) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an IP address is required when connecting by IP"})
			return
		}
		if i.UseIP == 0 && strings.TrimSpace(i.DNS) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a DNS name is required when connecting by DNS"})
			return
		}
		if i.Type == 2 && !i.Inherit && (i.SNMP == nil || i.SNMP.Version < 1 || i.SNMP.Version > 3) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an SNMP interface needs a version (1, 2 or 3)"})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	// Current interfaces, to know which ids exist (for update vs delete) and to reuse masked secrets.
	cur, err := s.zbx.HostDetail(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	curByID := map[string]zabbix.HostInterface{}
	for _, i := range cur.Interfaces {
		curByID[i.InterfaceID] = i
	}

	if err := s.zbx.UpdateHost(ctx, cur.HostID, req.Host, req.Name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	if err := s.zbx.SetHostProxy(ctx, cur.HostID, req.MonitoredBy, strings.TrimSpace(req.ProxyID)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}

	// SNMP inheritance: an interface set to "inherit" takes its creds from the host's (effective) proxy
	// default rather than the submitted values.
	effProxyID := ""
	if req.MonitoredBy == 1 {
		effProxyID = strings.TrimSpace(req.ProxyID)
	}
	var proxyDef store.SNMPDefault
	hasProxyDef := false
	if effProxyID != "" {
		proxyDef, hasProxyDef, _ = s.st.SNMPDefaultFor(ctx, effProxyID)
	}

	// Exactly one main interface per type: the first of each type in the submitted order.
	mainSeen := map[int]bool{}
	keep := map[string]bool{}
	for _, iv := range req.Interfaces {
		iface := zabbix.HostInterface{InterfaceID: iv.InterfaceID, Type: iv.Type, UseIP: iv.UseIP, IP: strings.TrimSpace(iv.IP), DNS: strings.TrimSpace(iv.DNS), Port: strings.TrimSpace(iv.Port)}
		if !mainSeen[iv.Type] {
			iface.Main = 1
			mainSeen[iv.Type] = true
		}
		if iface.Port == "" {
			iface.Port = defaultPort(iv.Type)
		}
		if iv.Type == 2 {
			if iv.Inherit {
				if effProxyID == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the host must be monitored by a proxy to inherit SNMP defaults"})
					return
				}
				if !hasProxyDef {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no SNMP default is set for this host's proxy — set one in the Probes tab, or override on the host"})
					return
				}
				iface.SNMP = defaultToDetails(proxyDef)
			} else if iv.SNMP != nil {
				iface.SNMP = viewToSNMP(iv.SNMP, curByID[iv.InterfaceID].SNMP)
			}
		}
		id := iv.InterfaceID
		if id != "" {
			keep[id] = true
			if err := s.zbx.UpdateHostInterface(ctx, iface); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
		} else {
			newID, err := s.zbx.CreateHostInterface(ctx, cur.HostID, iface)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
			id = newID
			keep[id] = true
		}
		if iv.Type == 2 {
			_ = s.st.SetSNMPInherit(ctx, id, iv.Inherit)
		}
	}
	// Delete interfaces the client dropped (and forget any inherit state).
	for _, i := range cur.Interfaces {
		if !keep[i.InterfaceID] {
			if err := s.zbx.DeleteHostInterface(ctx, i.InterfaceID); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
			_ = s.st.DeleteSNMPInherit(ctx, i.InterfaceID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSetHostProxy sets a host's collector (Server or a Proxy) - used both by the settings editor
// and the auto-switch offered after a host is moved into a site's group. Admin/helpdesk.
func (s *Server) handleSetHostProxy(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		MonitoredBy int    `json:"monitored_by"`
		ProxyID     string `json:"proxy_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.MonitoredBy == 1 && strings.TrimSpace(req.ProxyID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a proxy is required when monitored by a proxy"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := s.zbx.SetHostProxy(ctx, r.PathValue("id"), req.MonitoredBy, strings.TrimSpace(req.ProxyID)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// defaultPort returns the conventional port for an interface type when the client leaves it blank.
func defaultPort(ifaceType int) string {
	switch ifaceType {
	case 2:
		return "161" // SNMP
	case 3:
		return "623" // IPMI
	case 4:
		return "12345" // JMX
	default:
		return "10050" // Zabbix agent
	}
}

// viewToSNMP maps the browser SNMP shape to client details, carrying forward masked v3 passphrases
// (blank from the browser) from the currently-stored interface so they aren't wiped on save.
func viewToSNMP(v *snmpView, cur *zabbix.SNMPDetails) *zabbix.SNMPDetails {
	d := &zabbix.SNMPDetails{
		Version: v.Version, Community: v.Community, Bulk: v.Bulk,
		SecurityName: v.SecurityName, SecurityLevel: v.SecurityLevel,
		AuthProtocol: v.AuthProtocol, AuthPassphrase: v.AuthPassphrase,
		PrivProtocol: v.PrivProtocol, PrivPassphrase: v.PrivPassphrase,
		ContextName: v.ContextName,
	}
	if cur != nil {
		if d.AuthPassphrase == "" {
			d.AuthPassphrase = cur.AuthPassphrase
		}
		if d.PrivPassphrase == "" {
			d.PrivPassphrase = cur.PrivPassphrase
		}
	}
	return d
}
