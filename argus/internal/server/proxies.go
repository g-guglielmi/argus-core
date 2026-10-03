// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

type proxyView struct {
	Tags           []string `json:"tags"` // the probe's tags: every host it monitors carries them (tags.go)
	ID             string   `json:"id"`   // Zabbix proxyid, for "Monitored by" assignment
	Name           string   `json:"name"`
	LastAccess     int64    `json:"last_access"`     // unix seconds Zabbix last heard data, 0 if never seen
	Online         bool     `json:"online"`          // heard from within the last 2 minutes (before the 3-minute "not reporting" warning)
	Mode           string   `json:"mode"`            // active | passive
	EnrolledAt     int64    `json:"enrolled_at"`     // unix seconds a probe self-enrolled via Argus; 0 if manual
	Version        string   `json:"version"`         // running probe image version reported at check-in ("" = unknown)
	Target         string   `json:"target"`          // fleet target version this probe should converge on
	Latest         string   `json:"latest"`          // newest version resolved from GHCR ("" if unknown)
	SelfUpdate     bool     `json:"selfupdate"`      // an argus-updater sidecar is managing this probe
	Scans          bool     `json:"scans"`           // the probe advertises the network-scan capability (§B discovery)
	Sweeps         bool     `json:"sweeps"`          // ... and the UniFi-sweep capability
	UpdateStatus   string   `json:"update_status"`   // unknown | tracking | current | outdated | external
	LastCheckin    int64    `json:"last_checkin"`    // unix seconds of the last Argus check-in (0 = never)
	UpdaterVersion string   `json:"updater_version"` // version of the managing argus-updater sidecar ("" = none)
	UpdaterLatest  string   `json:"updater_latest"`  // newest argus-updater version resolved from GHCR ("" if unknown)
	UpdaterStatus  string   `json:"updater_status"`  // unknown | current | outdated (updater drift; "" when no sidecar)
	// An update Argus asked for, while it is in hand (nil: none): the proxy's, and its sidecar's own.
	UpdateJob      *probeJob `json:"update_job,omitempty"`
	UpdaterJob     *probeJob `json:"updater_job,omitempty"`
	BreakGlass     bool      `json:"break_glass"`      // a break-glass console credential exists (VM probes); reveal it via its own endpoint
	BreakGlassUser string    `json:"break_glass_user"` // the break-glass username ("" if none)
	// OS patch status a VM probe's host-side reporter posts (DESIGN §14c). SecUpdates is -1 until first
	// reported; OSReportedAt is 0 when the probe has never reported (non-VM probes never will).
	SecUpdates     int    `json:"sec_updates"`
	RebootRequired bool   `json:"reboot_required"`
	OSReportedAt   int64  `json:"os_reported_at"`
	OSVersion      string `json:"os_version"` // the probe VM's OS pretty-name ("" if not reported)
	// The probe's Argus-managed Probe health host: its id (for the link to its sensors; "" until it
	// exists) and its worst open problem: "ok" | "warning" | "error" ("" when there's no host).
	ProbeHostID string `json:"probe_host_id,omitempty"`
	ProbeHealth string `json:"probe_health,omitempty"`
	// Zabbix process counts (autoscale.go): each kind's running count, Argus's target and the busiest
	// hour at the last evaluation; whether a change waits for the probe's next start; the last change.
	Procs         []procView `json:"procs,omitempty"`
	ProcsPending  bool       `json:"procs_pending,omitempty"`
	ProcsNote     string     `json:"procs_note,omitempty"`
	ProcsNoteAt   int64      `json:"procs_note_at,omitempty"`
	ProcsSince    int64      `json:"procs_since,omitempty"`
	ProcsRestarts bool       `json:"procs_restarts,omitempty"` // the sidecar can restart the probe to apply a change
	Autoscale     string     `json:"autoscale,omitempty"`      // the install-wide mode
	// The probe's CPU as reported (0 = never): count, usable (a container limit), load averages; the
	// busiest hour's load per CPU at the last evaluation; whether it looked short on CPU.
	CPUCount   int       `json:"cpu_count,omitempty"`
	CPUUsable  float64   `json:"cpu_usable,omitempty"`
	CPULoad    []float64 `json:"cpu_load,omitempty"`
	CPUPeak    *float64  `json:"cpu_peak,omitempty"`
	CPUStarved bool      `json:"cpu_starved,omitempty"`
	IsVM       bool      `json:"is_vm,omitempty"` // a probe VM (it reports OS status), not a container on another host
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
	if sc := scopeFrom(r); !sc.all { // per-site visibility (scope.go): only the user's sites' probes
		kept := proxies[:0]
		for _, p := range proxies {
			if sc.coversGroup(probeSite(p.Name)) {
				kept = append(kept, p)
			}
		}
		proxies = kept
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
	probeHosts, probeHealth := s.probeHostHealth(ctx, proxies) // best-effort: empty maps on failure
	latest := s.probeLatest.get()                              // newest published probe version from GHCR ("" if unresolved)
	updaterLatest := s.updaterLatest.get()                     // newest published argus-updater version from GHCR
	autoscale := s.mgr.ProbeAutoscale()
	probeTags, _ := s.st.ProbeTags(ctx)
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
		// version - just the Zabbix version, without the -rN, and marked as externally managed.
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
			Online:         la > 0 && now-la <= 120,
			Mode:           mode,
			EnrolledAt:     enrolled[p.Name],
			Version:        version,
			Target:         target,
			Latest:         latest,
			SelfUpdate:     ag.SelfUpdate,
			Scans:          ag.Scans,
			Sweeps:         ag.Sweeps,
			UpdateStatus:   status,
			LastCheckin:    ag.LastCheckin,
			UpdaterVersion: ag.UpdaterVersion,
			UpdaterLatest:  updaterLatest,
			UpdaterStatus:  updaterViewStatus(ag, updaterLatest),
			UpdateJob:      s.probeJob(ctx, p.Name, ag.PendingUpdate, noticeProbePending, noticeProbeFailed, ag.Version, latest, now),
			UpdaterJob:     s.probeJob(ctx, p.Name, ag.PendingUpdaterUpdate, noticeUpdPending, noticeUpdFailed, ag.UpdaterVersion, updaterLatest, now),
			BreakGlass:     ag.BreakGlassSet,
			BreakGlassUser: ag.BreakGlassUser,
			SecUpdates:     osSecUpdates(ag),
			RebootRequired: ag.RebootRequired,
			OSReportedAt:   ag.OSReportedAt,
			OSVersion:      ag.OSVersion,
			ProbeHostID:    probeHosts[p.Name],
			ProbeHealth:    probeHealth[p.Name],
			Procs:          procViews(ag.Procs),
			ProcsPending:   ag.Procs.Pending(),
			ProcsNote:      ag.Procs.Note,
			ProcsNoteAt:    ag.Procs.DecidedAt,
			ProcsSince:     ag.Procs.Since,
			ProcsRestarts:  ag.Procs.Restarts,
			Autoscale:      autoscale,
			CPUCount:       ag.Procs.CPU.Count,
			CPUUsable:      ag.Procs.CPU.Effective(),
			CPULoad:        cpuLoadView(ag.Procs.CPU),
			CPUPeak:        cpuPeakView(ag.Procs.CPU),
			CPUStarved:     ag.Procs.CPU.Starved,
			IsVM:           ag.OSReportedAt > 0,
			Tags:           nonNilStrings(probeTags[p.ProxyID]),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// cpuLoadView is the reported 1/5/15-minute load averages, or nothing before the first report.
func cpuLoadView(c store.ProbeCPU) []float64 {
	if c.At == 0 {
		return nil
	}
	return []float64{c.Load1, c.Load5, c.Load15}
}

// cpuPeakView is the busiest hour's load per CPU at the last evaluation, or nothing when unknown.
func cpuPeakView(c store.ProbeCPU) *float64 {
	if c.Peak < 0 {
		return nil
	}
	v := c.Peak
	return &v
}

// probeHostHealth finds each proxy's Probe health host and its worst open problem, keyed by proxy
// name: two Zabbix calls for the whole fleet. Best-effort - on any failure the Probes page just
// shows no health link.
func (s *Server) probeHostHealth(ctx context.Context, proxies []zabbix.Proxy) (hostIDs, health map[string]string) {
	hostIDs, health = map[string]string{}, map[string]string{}
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		names = append(names, probeHostName(p.Name))
	}
	ids, err := s.zbx.HostIDsByNames(ctx, names)
	if err != nil {
		s.logger.Warn("proxies: Probe host lookup failed", "err", err)
		return
	}
	worst := map[string]int{} // host id -> highest open trigger priority
	if trigs, err := s.zbx.ActiveTriggers(ctx); err == nil {
		for _, t := range trigs {
			pr, _ := strconv.Atoi(t.Priority)
			for _, h := range t.Hosts {
				if pr > worst[h.HostID] {
					worst[h.HostID] = pr
				}
			}
		}
	} else {
		s.logger.Warn("proxies: active trigger lookup failed", "err", err)
	}
	for _, p := range proxies {
		id := ids[probeHostName(p.Name)]
		if id == "" {
			continue
		}
		hostIDs[p.Name] = id
		switch w := worst[id]; {
		case w >= 3:
			health[p.Name] = "error"
		case w == 2:
			health[p.Name] = "warning"
		default:
			health[p.Name] = "ok"
		}
	}
	return
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

// updaterViewStatus returns the updater-drift status for the proxy view: "" when no argus-updater
// sidecar manages this probe (nothing to show), otherwise updaterStatus (unknown/current/outdated).
func updaterViewStatus(ag store.ProbeAgent, latest string) string {
	if !ag.SelfUpdate {
		return ""
	}
	return updaterStatus(ag.UpdaterVersion, latest)
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
	// The Argus-managed Probe health host goes first: Zabbix refuses to delete a proxy that still
	// monitors hosts, and that host is monitored by this proxy.
	if err := s.deleteProbeHost(ctx, name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not remove the probe's health host: " + err.Error()})
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
