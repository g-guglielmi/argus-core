// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
)

// OS patching & lifecycle (DESIGN §14c). The Debian OS under the core VM and every probe VM patches
// itself locally via unattended-upgrades (security suite only); Argus never triggers an apt upgrade
// remotely (no clean rollback). This file is the *visibility + scheduling* half:
//
//   - probe VMs POST their pending security-update count + reboot-required flag here (host-side timer,
//     probe-token auth) - stored per proxy and surfaced on the Probes view;
//   - the core VM's own status is written by a host-side timer into the shared update dir
//     (os-status.json), which Argus reads;
//   - the core reboot is operator-scheduled: an admin picks a day+time (or "notify only") and Argus
//     mirrors it to reboot-window.json in the update dir for a host timer to honour locally.

// coreOSStatusFile is where the core VM's host-side reporter writes its OS patch status (in the shared
// ARGUS_UPDATE_DIR, alongside the self-update channel files).
const coreOSStatusFile = "os-status.json"

// rebootWindowFile is where Argus mirrors the operator-chosen core reboot window for the host timer.
const rebootWindowFile = "reboot-window.json"

// rebootWindowKey is the app_meta key holding the core reboot window (JSON) as the source of truth.
const rebootWindowKey = "core_reboot_window"

// zbxWindowFile is where Argus mirrors the operator-chosen Zabbix minor-update window for the host
// timer (argus-zbx-update). Same shape as the reboot window; a separate policy on purpose - an
// admin may happily auto-reboot weekly yet want Zabbix updates notify-only, or the reverse.
const zbxWindowFile = "zbx-update-window.json"

// zbxWindowKey is the app_meta key holding the Zabbix minor-update window (JSON).
const zbxWindowKey = "core_zbx_update_window"

// rebootWindow is the operator-scheduled reboot policy for the core VM. Mode "notify" never reboots
// unattended (Argus just flags reboot-required); mode "auto" reboots in the given weekly window when
// the OS needs it. Weekday is 0=Sunday..6=Saturday.
type rebootWindow struct {
	Mode    string `json:"mode"`    // "notify" | "auto"
	Weekday int    `json:"weekday"` // 0..6 (auto only)
	Hour    int    `json:"hour"`    // 0..23 (auto only)
	Minute  int    `json:"minute"`  // 0..59 (auto only)
}

// defaultRebootWindow is the safe default for the core (a pet that hosts the DB + Zabbix data plane):
// notify only, never bounce unannounced.
func defaultRebootWindow() rebootWindow { return rebootWindow{Mode: "notify"} }

// valid reports whether the window is well-formed (so a bad PUT is rejected before it's stored).
func (rw rebootWindow) valid() bool {
	if rw.Mode != "notify" && rw.Mode != "auto" {
		return false
	}
	if rw.Mode == "auto" {
		return rw.Weekday >= 0 && rw.Weekday <= 6 && rw.Hour >= 0 && rw.Hour <= 23 && rw.Minute >= 0 && rw.Minute <= 59
	}
	return true
}

// loadRebootWindow reads the stored window (the default when unset or unparseable).
func (s *Server) loadRebootWindow(ctx context.Context) rebootWindow {
	raw, ok, err := s.st.MetaGet(ctx, rebootWindowKey)
	if err != nil || !ok || raw == "" {
		return defaultRebootWindow()
	}
	var rw rebootWindow
	if json.Unmarshal([]byte(raw), &rw) != nil || !rw.valid() {
		return defaultRebootWindow()
	}
	return rw
}

// syncRebootWindowFile mirrors the stored window to reboot-window.json in the shared update dir, so the
// host reboot timer honours it locally. No-op when the update dir isn't wired (self-update disabled).
func (s *Server) syncRebootWindowFile(ctx context.Context) {
	if !s.cfg.SelfUpdateEnabled() {
		return
	}
	if err := s.writeUpdateJSONAtomic(rebootWindowFile, s.loadRebootWindow(ctx)); err != nil {
		s.logger.Warn("reboot window: could not mirror to update dir", "err", err)
	}
}

// loadZbxWindow reads the stored Zabbix minor-update window (notify-only by default). The reboot
// window's struct and validation are reused - the policy shape is identical.
func (s *Server) loadZbxWindow(ctx context.Context) rebootWindow {
	raw, ok, err := s.st.MetaGet(ctx, zbxWindowKey)
	if err != nil || !ok || raw == "" {
		return defaultRebootWindow()
	}
	var rw rebootWindow
	if json.Unmarshal([]byte(raw), &rw) != nil || !rw.valid() {
		return defaultRebootWindow()
	}
	return rw
}

// syncZbxWindowFile mirrors the stored Zabbix update window to the shared update dir for the
// host's argus-zbx-update timer. Same no-op rule as the reboot window.
func (s *Server) syncZbxWindowFile(ctx context.Context) {
	if !s.cfg.SelfUpdateEnabled() {
		return
	}
	if err := s.writeUpdateJSONAtomic(zbxWindowFile, s.loadZbxWindow(ctx)); err != nil {
		s.logger.Warn("zabbix update window: could not mirror to update dir", "err", err)
	}
}

// handleReportProbeOSStatus receives a probe VM's OS patch status (public; authenticated by the same
// long-lived probe token as check-in). A host-side timer on the VM reports the pending security-update
// count and whether the OS wants a reboot; patching itself stays local.
func (s *Server) handleReportProbeOSStatus(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	if tok == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing probe token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	proxyName, err := s.st.ProbeNameByToken(ctx, auth.HashToken(tok))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe token"})
		return
	}
	var req struct {
		SecUpdates     *int   `json:"sec_updates"`
		RebootRequired bool   `json:"reboot_required"`
		OS             string `json:"os"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	count := -1
	if req.SecUpdates != nil {
		count = *req.SecUpdates
	}
	if err := s.st.SetProbeOSStatus(ctx, proxyName, count, req.RebootRequired, req.OS); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store OS status"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// coreOSView is the core VM's own OS patch status, read from the host-side reporter's file.
type coreOSView struct {
	Available      bool   `json:"available"`       // the update dir is wired and a report was found
	SecUpdates     int    `json:"sec_updates"`     // pending security updates (-1 = unknown)
	RebootRequired bool   `json:"reboot_required"` // /var/run/reboot-required present
	ReportedAt     int64  `json:"reported_at"`     // unix seconds the host last reported
	OS             string `json:"os,omitempty"`    // e.g. "Debian GNU/Linux 13 (trixie)"
	// Zabbix server package versions (host-installed; reporter r2+): installed and the apt
	// candidate, normalized (no epoch/revision). Both empty when Zabbix isn't on the host or the
	// reporter predates them.
	ZbxServer    string `json:"zbx_server,omitempty"`
	ZbxCandidate string `json:"zbx_candidate,omitempty"`
}

// osStatusResponse drives the OS-patching UI: the core's own status and the operator windows.
// Per-probe patch status rides along on /api/proxies (the fleet view already fetches it).
type osStatusResponse struct {
	Core         coreOSView   `json:"core"`
	RebootWindow rebootWindow `json:"reboot_window"`
	ZbxWindow    rebootWindow `json:"zbx_window"`
	FleetZbx     string       `json:"fleet_zbx,omitempty"` // newest Zabbix version among the probes
}

// coreOSStatus reads the core VM's OS status file from the shared update dir.
func (s *Server) coreOSStatus() coreOSView {
	v := coreOSView{SecUpdates: -1}
	if !s.cfg.SelfUpdateEnabled() {
		return v
	}
	var rec struct {
		SecUpdates     int    `json:"sec_updates"`
		RebootRequired bool   `json:"reboot_required"`
		ReportedAt     int64  `json:"reported_at"`
		OS             string `json:"os"`
		ZbxServer      string `json:"zbx_server"`
		ZbxCandidate   string `json:"zbx_candidate"`
	}
	ok, err := readUpdateJSON(s.updatePath(coreOSStatusFile), &rec)
	if err != nil || !ok {
		return v
	}
	v.Available = true
	v.SecUpdates = rec.SecUpdates
	v.RebootRequired = rec.RebootRequired
	v.ReportedAt = rec.ReportedAt
	v.OS = rec.OS
	v.ZbxServer = rec.ZbxServer
	v.ZbxCandidate = rec.ZbxCandidate
	return v
}

// fleetZbxVersion returns the newest Zabbix version the probe fleet reports (the image versions
// are "<zabbix>-rN"; the prefix before the revision is the Zabbix version). "" when no probe has
// reported one. Newest by simple numeric compare of dotted parts - Zabbix versions are x.y.z.
func fleetZbxVersion(versions []string) string {
	best, bestParts := "", []int(nil)
	for _, v := range versions {
		zv := v
		if i := strings.IndexByte(zv, '-'); i >= 0 {
			zv = zv[:i]
		}
		parts := strings.Split(zv, ".")
		if len(parts) != 3 {
			continue
		}
		nums := make([]int, 3)
		ok := true
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				ok = false
				break
			}
			nums[i] = n
		}
		if !ok {
			continue
		}
		newer := best == ""
		for i := 0; !newer && i < 3; i++ {
			if nums[i] != bestParts[i] {
				newer = nums[i] > bestParts[i]
				break
			}
		}
		if newer {
			best, bestParts = zv, nums
		}
	}
	return best
}

// handleOSStatus returns the core's OS patch status and the configured operator windows
// (authenticated). The fleet's newest probe Zabbix version rides along so the UI can show
// core-vs-fleet drift.
func (s *Server) handleOSStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	fleet := ""
	if agents, err := s.st.ProbeAgents(ctx); err == nil {
		versions := make([]string, 0, len(agents))
		for _, a := range agents {
			versions = append(versions, a.Version)
		}
		fleet = fleetZbxVersion(versions)
	}
	writeJSON(w, http.StatusOK, osStatusResponse{
		Core:         s.coreOSStatus(),
		RebootWindow: s.loadRebootWindow(ctx),
		ZbxWindow:    s.loadZbxWindow(ctx),
		FleetZbx:     fleet,
	})
}

// handleSetRebootWindow stores the operator-chosen core reboot window and mirrors it to the update dir
// for the host timer (admin). Patching stays local: this only schedules the *reboot*, never a remote apt run.
func (s *Server) handleSetRebootWindow(w http.ResponseWriter, r *http.Request) {
	var rw rebootWindow
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&rw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if !rw.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "notify" or "auto"; for auto, weekday 0-6, hour 0-23, minute 0-59`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b, _ := json.Marshal(rw)
	if err := s.st.MetaSet(ctx, rebootWindowKey, string(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the reboot window"})
		return
	}
	s.syncRebootWindowFile(ctx)
	s.logger.Info("core reboot window set", "mode", rw.Mode, "weekday", rw.Weekday, "hour", rw.Hour, "minute", rw.Minute)
	writeJSON(w, http.StatusOK, rw)
}

// handleSetZbxWindow stores the operator-chosen Zabbix minor-update window and mirrors it to the
// update dir for the host's argus-zbx-update timer (admin). Same local-only principle: Argus never
// runs apt remotely - the host applies same-major zabbix-* updates in this window by itself, and
// major upgrades always stay a planned manual event.
func (s *Server) handleSetZbxWindow(w http.ResponseWriter, r *http.Request) {
	var rw rebootWindow
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&rw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if !rw.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "notify" or "auto"; for auto, weekday 0-6, hour 0-23, minute 0-59`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b, _ := json.Marshal(rw)
	if err := s.st.MetaSet(ctx, zbxWindowKey, string(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the update window"})
		return
	}
	s.syncZbxWindowFile(ctx)
	s.logger.Info("core zabbix update window set", "mode", rw.Mode, "weekday", rw.Weekday, "hour", rw.Hour, "minute", rw.Minute)
	writeJSON(w, http.StatusOK, rw)
}
