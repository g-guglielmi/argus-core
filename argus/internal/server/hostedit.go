// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"argus/internal/provision"
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

// macroFieldView is one class-declared per-host macro shown in the settings editor: its spec plus the
// host's current value. A secret macro's value is masked (blank) and only Set signals it's configured.
type macroFieldView struct {
	Macro   string   `json:"macro"`
	Label   string   `json:"label"`
	Hint    string   `json:"hint,omitempty"`
	Secret  bool     `json:"secret,omitempty"`
	Options []string `json:"options,omitempty"` // fixed value set: the editor renders a select (blank = template default)
	Value   string   `json:"value"`             // current host value ("" if unset, or masked for a secret)
	Set     bool     `json:"set,omitempty"`     // a value is configured on the host (for secrets, where Value is masked)
}

type hostConfigView struct {
	HostID       string           `json:"hostid"`
	Host         string           `json:"host"`         // technical name
	Name         string           `json:"name"`         // visible name
	MonitoredBy  int              `json:"monitored_by"` // 0 server, 1 proxy, 2 proxy group
	ProxyID      string           `json:"proxy_id,omitempty"`
	ProxyName    string           `json:"proxy_name,omitempty"`
	ProxyDefault *snmpView        `json:"proxy_default,omitempty"` // the host's proxy SNMP default (masked), if set
	Interfaces   []ifaceView      `json:"interfaces"`
	ClassID      string           `json:"class_id,omitempty"`    // device class, when known
	ClassLabel   string           `json:"class_label,omitempty"` // human label for the class macro section
	Macros       []macroFieldView `json:"macros,omitempty"`      // class-declared per-host macros + current values
	VMNames      []string         `json:"vm_names,omitempty"`    // xcpng: discovered VM names, for the ignored-VMs checklist
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
	} else if def, ok, _ := s.st.SNMPDefaultFor(ctx, "0"); ok {
		out.ProxyDefault = defaultToView(def) // server-monitored: the core's own SNMP default ("0")
	}
	inherit, _ := s.st.SNMPInheritMap(ctx)
	for _, i := range hd.Interfaces {
		out.Interfaces = append(out.Interfaces, ifaceView{InterfaceID: i.InterfaceID, Type: i.Type, UseIP: i.UseIP, IP: i.IP, DNS: i.DNS, Port: i.Port, SNMP: snmpToView(i.SNMP), Inherit: i.Type == 2 && inherit[i.InterfaceID]})
	}

	// Class-declared per-host macros (e.g. Windows service matching): show each spec with the host's
	// current value so the editor can tune them after creation.
	if classID, ok, _ := s.st.GetDeviceClass(ctx, hd.HostID); ok {
		if class, ok := provision.ClassByID(classID); ok && len(class.Macros) > 0 {
			out.ClassID = class.ID
			out.ClassLabel = class.Label
			cur := map[string]zabbix.HostMacro{}
			if hm, err := s.zbx.HostMacros(ctx, hd.HostID); err == nil {
				for _, m := range hm {
					cur[m.Macro] = m
				}
			}
			for _, ms := range class.Macros {
				f := macroFieldView{Macro: ms.Macro, Label: ms.Label, Hint: ms.Hint, Secret: ms.Secret, Options: ms.Options}
				if m, has := cur[ms.Macro]; has {
					f.Set = strings.TrimSpace(m.Value) != "" || ms.Secret
					if !ms.Secret {
						f.Value = m.Value
					}
				}
				out.Macros = append(out.Macros, f)
			}
			// The XCP-NG ignored-VMs checklist needs names to pick from: the VMs currently
			// discovered as state sensors (present once {$XCP.VM.MODE} is state/full). Names
			// already ignored are merged back in by the frontend from the macro value itself.
			if class.ID == "xcpng" {
				if items, err := s.zbx.Items(ctx, hd.HostID); err == nil {
					for _, it := range items {
						if b, _ := splitKey(it.Key); b == "xcp.vm.state" {
							if n := xcpVMInstance(it.Name, " state", ""); n != "" {
								out.VMNames = append(out.VMNames, n)
							}
						}
					}
					sort.Strings(out.VMNames)
				}
			}
		}
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
		Host        string            `json:"host"`
		Name        string            `json:"name"`
		MonitoredBy int               `json:"monitored_by"`
		ProxyID     string            `json:"proxy_id"`
		Interfaces  []ifaceView       `json:"interfaces"`
		Macros      map[string]string `json:"macros"` // class macro name -> desired value (only declared macros are applied)
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

	// SNMP inheritance: an interface set to "inherit" takes its creds from the host's (effective)
	// collector default rather than the submitted values - the proxy's, or the core server's own
	// default (stored under proxy id "0") for server-monitored hosts.
	effProxyID := "0"
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
	survivorByType := map[int]string{} // a surviving interface id per type, to move items onto before a delete
	anySurvivor := ""
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
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick the proxy this host is monitored by before inheriting SNMP defaults"})
					return
				}
				if !hasProxyDef {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no SNMP default is set for this host's collector - set one in the Probes tab (the probe's Defaults, or Core SNMP), or override on the host"})
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
		if _, ok := survivorByType[iv.Type]; !ok {
			survivorByType[iv.Type] = id
		}
		if anySurvivor == "" {
			anySurvivor = id
		}
	}
	// Delete interfaces the client dropped. Zabbix refuses to delete an interface with items still on
	// it, so first move those items to a surviving interface (same type if possible, else any) - this
	// is what lets you, e.g., swap a host's Agent interface for an SNMP one without stranding ICMP ping.
	for _, i := range cur.Interfaces {
		if !keep[i.InterfaceID] {
			target := survivorByType[i.Type]
			if target == "" {
				target = anySurvivor
			}
			if target != "" {
				items, ierr := s.zbx.ItemsOnInterface(ctx, i.InterfaceID)
				if ierr != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + ierr.Error()})
					return
				}
				for _, itemID := range items {
					if err := s.zbx.SetItemInterface(ctx, itemID, target); err != nil {
						writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: could not move an item off the interface being removed (it may need an interface of the same type): " + err.Error()})
						return
					}
				}
			}
			if err := s.zbx.DeleteHostInterface(ctx, i.InterfaceID); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
			_ = s.st.DeleteSNMPInherit(ctx, i.InterfaceID)
		}
	}

	// Class-declared per-host macros (only the ones the class defines are touched, so preset macros
	// and template defaults are left alone).
	if req.Macros != nil {
		if err := s.applyClassMacros(ctx, cur.HostID, req.Macros); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// applyClassMacros surgically sets/clears the host macros a device class declares, from the desired
// map (macro name -> value). Only class-declared macros are touched. A cleared text macro is deleted
// (reverting to the template default); a secret macro left blank is kept unchanged (its value can't
// be read back to compare). Macros the class doesn't declare, and preset macros, are never touched.
func (s *Server) applyClassMacros(ctx context.Context, hostID string, desired map[string]string) error {
	classID, ok, _ := s.st.GetDeviceClass(ctx, hostID)
	if !ok {
		return nil
	}
	class, ok := provision.ClassByID(classID)
	if !ok || len(class.Macros) == 0 {
		return nil
	}
	cur := map[string]zabbix.HostMacro{}
	hm, err := s.zbx.HostMacros(ctx, hostID)
	if err != nil {
		return err
	}
	for _, m := range hm {
		cur[m.Macro] = m
	}
	for _, ms := range class.Macros {
		v, sent := desired[ms.Macro]
		if !sent {
			continue // the client didn't include this macro; leave it as-is
		}
		v = strings.TrimSpace(v)
		existing, has := cur[ms.Macro]
		mType := 0
		if ms.Secret {
			mType = 1
		}
		switch {
		case ms.Secret && v == "":
			// Blank secret = unchanged (we can't read the stored value to diff it).
			continue
		case v == "":
			// Cleared text macro: drop it so the template default applies again.
			if has {
				if err := s.zbx.DeleteHostMacros(ctx, existing.MacroID); err != nil {
					return err
				}
			}
		case has:
			if ms.Secret || existing.Value != v {
				if err := s.zbx.UpdateHostMacro(ctx, existing.MacroID, v, mType); err != nil {
					return err
				}
			}
		default:
			if err := s.zbx.CreateHostMacro(ctx, hostID, zabbix.Macro{Macro: ms.Macro, Value: v, Type: mType}); err != nil {
				return err
			}
		}
	}
	// An ignored VM's "not running" problem can never recover on its own: the next poll drops the
	// VM from the blob, discovery disables its state item, and no recovery value ever arrives. The
	// triggers allow manual close, so close those problems here (best-effort - a failure just
	// leaves the manual route).
	if v, sent := desired["{$XCP.VM.IGNORE}"]; sent && classID == "xcpng" {
		s.closeIgnoredVMProblems(ctx, hostID, v)
	}
	return nil
}

// closeIgnoredVMProblems closes the open "VM <name> is not running" problems for every name on the
// XCP-NG ignore list.
func (s *Server) closeIgnoredVMProblems(ctx context.Context, hostID, ignoreCSV string) {
	names := map[string]bool{}
	for _, n := range strings.Split(ignoreCSV, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names[n] = true
		}
	}
	if len(names) == 0 {
		return
	}
	probs, err := s.zbx.Problems(ctx, hostID)
	if err != nil {
		return
	}
	for _, p := range probs {
		if !strings.HasPrefix(p.Name, "VM ") || !strings.HasSuffix(p.Name, " is not running") {
			continue
		}
		if names[strings.TrimSuffix(strings.TrimPrefix(p.Name, "VM "), " is not running")] {
			_ = s.zbx.CloseEvent(ctx, p.EventID, "VM ignored in Argus host settings")
		}
	}
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
