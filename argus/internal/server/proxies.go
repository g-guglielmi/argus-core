package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/store"
)

type proxyView struct {
	ID             string `json:"id"` // Zabbix proxyid, for "Monitored by" assignment
	Name           string `json:"name"`
	LastAccess     int64  `json:"last_access"`      // unix seconds Zabbix last heard data, 0 if never seen
	Online         bool   `json:"online"`           // seen within the last 5 minutes
	Mode           string `json:"mode"`             // active | passive
	EnrolledAt     int64  `json:"enrolled_at"`      // unix seconds a probe self-enrolled via Argus; 0 if manual
	Version        string `json:"version"`          // running probe image version reported at check-in ("" = unknown)
	Target         string `json:"target"`           // fleet target version this probe should converge on
	Latest         string `json:"latest"`           // newest version resolved from GHCR ("" if unknown)
	SelfUpdate     bool   `json:"selfupdate"`       // an argus-updater sidecar is managing this probe
	UpdateStatus   string `json:"update_status"`    // unknown | tracking | current | outdated | external
	LastCheckin    int64  `json:"last_checkin"`     // unix seconds of the last Argus check-in (0 = never)
	UpdaterVersion string `json:"updater_version"`  // version of the managing argus-updater sidecar ("" = none)
	BreakGlass     bool   `json:"break_glass"`      // a break-glass console credential exists (VM probes); reveal it via its own endpoint
	BreakGlassUser string `json:"break_glass_user"` // the break-glass username ("" if none)
	// OS patch status a VM probe's host-side reporter posts (DESIGN §14c). SecUpdates is -1 until first
	// reported; OSReportedAt is 0 when the probe has never reported (non-VM probes never will).
	SecUpdates     int   `json:"sec_updates"`
	RebootRequired bool  `json:"reboot_required"`
	OSReportedAt   int64 `json:"os_reported_at"`
}

// handleProxies lists Zabbix proxies (the per-site collectors) with their last-access time, so
// the Probes view can show which sites are actually reporting instead of placeholder data.
func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	enrolled, err := s.st.EnrollmentTimes(ctx) // best-effort; a nil map still indexes safely below
	if err != nil {
		s.logger.Warn("proxies: enrollment times lookup failed", "err", err)
	}
	agents, err := s.st.ProbeAgents(ctx) // best-effort fleet-update state (version / self-update)
	if err != nil {
		s.logger.Warn("proxies: probe agents lookup failed", "err", err)
	}
	target, err := s.st.ProbeTargetVersion(ctx)
	if err != nil {
		s.logger.Warn("proxies: probe target lookup failed", "err", err)
		target = "latest"
	}
	latest := s.probeLatest.get() // newest published probe version from GHCR ("" if unresolved)
	now := time.Now().Unix()
	out := make([]proxyView, 0, len(proxies))
	for _, p := range proxies {
		la := atoi64(p.LastAccess)
		mode := "active"
		if p.Mode == "1" {
			mode = "passive"
		}
		ag := agents[p.Name]
		// Prefer the precise version a fleet-aware probe self-reports (includes our wrapper
		// revision, e.g. 7.0.29-r2). Fall back to the Zabbix-reported proxy version so probes that
		// don't check in (older images, or ones updated outside Argus like unRAID) still show a
		// version — just the Zabbix version, without the -rN, and marked as externally managed.
		version, status := ag.Version, updateStatus(ag.Version, target, latest)
		if version == "" {
			if zv := zbxVersionString(p.Version); zv != "" {
				version, status = zv, "external"
			}
		}
		out = append(out, proxyView{
			ID:             p.ProxyID,
			Name:           p.Name,
			LastAccess:     la,
			Online:         la > 0 && now-la <= 300,
			Mode:           mode,
			EnrolledAt:     enrolled[p.Name],
			Version:        version,
			Target:         target,
			Latest:         latest,
			SelfUpdate:     ag.SelfUpdate,
			UpdateStatus:   status,
			LastCheckin:    ag.LastCheckin,
			UpdaterVersion: ag.UpdaterVersion,
			BreakGlass:     ag.BreakGlassSet,
			BreakGlassUser: ag.BreakGlassUser,
			SecUpdates:     osSecUpdates(ag),
			RebootRequired: ag.RebootRequired,
			OSReportedAt:   ag.OSReportedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// osSecUpdates normalises a probe's reported security-update count. A probe that has never reported OS
// status (os_reported_at == 0, incl. the zero-value agent for a probe that never enrolled) reads as -1
// (unknown) rather than the Go zero 0, which would look like "fully patched".
func osSecUpdates(ag store.ProbeAgent) int {
	if ag.OSReportedAt == 0 {
		return -1
	}
	return ag.SecUpdates
}

// zbxVersionString normalises Zabbix's proxy version field to a dotted string. Zabbix reports it
// either already dotted ("7.0.29") or as a packed integer ("70029" = major*10000 + minor*100 +
// patch); "" and "0" mean the proxy has never connected.
func zbxVersionString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return ""
	}
	if strings.Contains(raw, ".") {
		return raw
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", n/10000, (n/100)%100, n%100)
}

// handleDeleteProxy removes a proxy from Zabbix (proxy.delete) and cleans up the Argus-side records
// it leaves behind (enroll tokens, check-in/version state, SNMP default). Admin only. Zabbix refuses
// to delete a proxy that still monitors hosts - that error is surfaced so the operator can reassign
// them first. The proxy's host group (proxy-<site>) is left in place; delete or hide it from the tree.
func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured"})
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Resolve the proxy's name (name-keyed Argus records need it).
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	name := ""
	for _, p := range proxies {
		if p.ProxyID == id {
			name = p.Name
			break
		}
	}
	if name == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "proxy not found"})
		return
	}
	if err := s.zbx.DeleteProxy(ctx, id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	if err := s.st.DeleteProxyRecords(ctx, id, name); err != nil {
		s.logger.Warn("delete proxy: record cleanup failed", "proxy", name, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "deleted": name})
}

// handleReconcileProxies prunes Argus records orphaned by proxies deleted directly in Zabbix (out of
// band). Admin only. Returns how many rows were pruned.
func (s *Server) handleReconcileProxies(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	names := make(map[string]bool, len(proxies))
	ids := make(map[string]bool, len(proxies))
	for _, p := range proxies {
		names[p.Name] = true
		ids[p.ProxyID] = true
	}
	pruned, err := s.st.ReconcileProxies(ctx, names, ids)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cleanup failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pruned": pruned})
}
