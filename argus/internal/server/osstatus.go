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

// The requests Argus drops for the host when an admin clicks Update now (a pending Zabbix minor),
// Reboot now (the OS asked for a reboot) or Patch now (pending security updates): a path unit starts
// the matching host script within seconds, and the script removes the file as it starts. Each
// leaves a result file afterwards, done, failed or rebooting, why it ran (request or window) and
// one sentence, which the Updates page shows. Argus never runs apt or reboots anything itself: the
// host applies, Argus asks and reads.
const (
	zbxUpdateRequestFile = "zbx-update-request.json"
	zbxUpdateResultFile  = "zbx-update-result.json"
	rebootRequestFile    = "reboot-request.json"
	rebootResultFile     = "reboot-result.json"
	patchRequestFile     = "patch-request.json"
	patchResultFile      = "patch-result.json"
)

// hostActionResult is the host's account of the last action of one kind (a *-result.json).
type hostActionResult struct {
	State  string `json:"state"`          // done | failed | rebooting
	From   string `json:"from,omitempty"` // versions, for the Zabbix update
	To     string `json:"to,omitempty"`
	At     int64  `json:"at"`
	Why    string `json:"why"` // request | window
	Detail string `json:"detail"`
}

// tzFile is where Argus mirrors the configured timezone for the host timer (argus-tz-check
// applies it via timedatectl after validating against the zoneinfo database). The source of
// truth is the ordinary Settings -> General -> Timezone setting (ARGUS_TZ): ONE timezone drives
// both the app's timestamps and the core VM's clock.
const tzFile = "timezone.json"

// tzSetting is the timezone.json payload.
type tzSetting struct {
	TZ string `json:"tz"`
}

// validTimezone is a light shape check on an IANA zone name ("Europe/Rome"); the host is the
// real gatekeeper (it only applies names present in its zoneinfo database).
func validTimezone(tz string) bool {
	if tz == "" || len(tz) > 64 {
		return false
	}
	for i, r := range tz {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '_' || r == '+' || r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

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

// syncTimezoneFile mirrors the configured timezone (Settings -> General, ARGUS_TZ) for the
// host's argus-tz-check timer. Nothing is written while the setting sits on its built-in
// default, so a fresh install never overrides the VM's first-boot timezone with UTC.
func (s *Server) syncTimezoneFile(context.Context) {
	if !s.cfg.SelfUpdateEnabled() {
		return
	}
	tz, configured := s.mgr.ConfiguredTimezone()
	if !configured || !validTimezone(tz) {
		return
	}
	if err := s.writeUpdateJSONAtomic(tzFile, tzSetting{TZ: tz}); err != nil {
		s.logger.Warn("timezone: could not mirror to update dir", "err", err)
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
	if err := s.st.SetProbeOSStatus(ctx, proxyName, count, req.RebootRequired, clip(printable(req.OS), 80)); err != nil {
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
	// VM timezone + NTP sync state (reporter r3+). ClockSync is nil when the reporter predates
	// the field or timedatectl gave no answer - the UI then shows nothing rather than a false
	// warning.
	TZ        string `json:"tz,omitempty"`
	ClockSync *bool  `json:"clock_sync,omitempty"`
	// Update now, Reboot now, Patch now (reporter r4+): the host honours a request file for each.
	// The *Pending fields carry a request's time while the host has not picked it up yet; ZbxUpdate,
	// Reboot and Patch are the host's account of the last action of that kind.
	ZbxUpdateNow     bool              `json:"zbx_update_now"`
	RebootNow        bool              `json:"reboot_now"`
	PatchNow         bool              `json:"patch_now"`
	ZbxUpdatePending int64             `json:"zbx_update_pending,omitempty"`
	RebootPending    int64             `json:"reboot_pending,omitempty"`
	PatchPending     int64             `json:"patch_pending,omitempty"`
	ZbxUpdate        *hostActionResult `json:"zbx_update,omitempty"`
	Reboot           *hostActionResult `json:"reboot,omitempty"`
	Patch            *hostActionResult `json:"patch,omitempty"`
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
		TZ             string `json:"tz"`
		Clock          string `json:"clock"` // timedatectl NTPSynchronized: "yes" | "no" | ""
		ZbxUpdateNow   bool   `json:"zbx_update_now"`
		RebootNow      bool   `json:"reboot_now"`
		PatchNow       bool   `json:"patch_now"`
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
	v.TZ = rec.TZ
	if rec.Clock == "yes" || rec.Clock == "no" {
		synced := rec.Clock == "yes"
		v.ClockSync = &synced
	}
	v.ZbxUpdateNow, v.RebootNow, v.PatchNow = rec.ZbxUpdateNow, rec.RebootNow, rec.PatchNow
	v.ZbxUpdate, v.ZbxUpdatePending = s.hostAction(zbxUpdateResultFile, zbxUpdateRequestFile)
	v.Reboot, v.RebootPending = s.hostAction(rebootResultFile, rebootRequestFile)
	v.Patch, v.PatchPending = s.hostAction(patchResultFile, patchRequestFile)
	return v
}

// hostAction reads one kind's last result and, when its request file still waits for the host, the
// request's time.
func (s *Server) hostAction(resultFile, requestFile string) (*hostActionResult, int64) {
	var res hostActionResult
	var out *hostActionResult
	if ok, err := readUpdateJSON(s.updatePath(resultFile), &res); err == nil && ok && res.State != "" {
		out = &res
	}
	var req struct {
		At int64 `json:"at"`
	}
	pending := int64(0)
	if ok, err := readUpdateJSON(s.updatePath(requestFile), &req); err == nil && ok {
		pending = req.At
	}
	return out, pending
}

// hostRequestRefusal says why a request of one kind ("zbx-update", "reboot", "patch") can't go ahead,
// "" when it can. A request the host never picked up stops blocking after 15 minutes (the path unit
// was not installed, or the VM was off).
func hostRequestRefusal(kind string, v coreOSView, now int64) string {
	if !v.Available {
		return "the core's host reporter is not wired up (deploy/core/setup-core-patching.sh)"
	}
	stale := func(pending int64) bool { return pending > 0 && now-pending < 15*60 }
	switch kind {
	case "zbx-update":
		switch {
		case !v.ZbxUpdateNow:
			return "the core's host scripts predate Update now: re-run deploy/core/setup-core-patching.sh on the core VM, or let the window apply it"
		case v.ZbxServer == "":
			return "the host reports no Zabbix server package"
		case v.ZbxCandidate == "" || v.ZbxCandidate == v.ZbxServer:
			return "Zabbix on the core is up to date"
		case stale(v.ZbxUpdatePending):
			return "an update is already requested; the host applies it within a minute"
		}
	case "reboot":
		switch {
		case !v.RebootNow:
			return "the core's host scripts predate Reboot now: re-run deploy/core/setup-core-patching.sh on the core VM, or let the window reboot it"
		case !v.RebootRequired:
			return "the core does not need a reboot"
		case stale(v.RebootPending):
			return "a reboot is already requested"
		}
	case "patch":
		switch {
		case !v.PatchNow:
			return "the core's host scripts predate Patch now: re-run deploy/core/setup-core-patching.sh on the core VM; the updates apply by themselves tonight"
		case v.SecUpdates <= 0:
			return "no security updates are pending"
		case stale(v.PatchPending):
			return "patching is already requested"
		}
	default:
		return "unknown request"
	}
	return ""
}

// requestHostAction drops one kind's request file for the host (admin): the path unit on the host
// starts the matching script, which reports back through os-status.json and its result file.
func (s *Server) requestHostAction(w http.ResponseWriter, r *http.Request, kind, file string, extra map[string]any) {
	v := s.coreOSStatus()
	if why := hostRequestRefusal(kind, v, time.Now().Unix()); why != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": why})
		return
	}
	by := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		by = u.Email
	}
	at := time.Now().Unix()
	req := map[string]any{"at": at, "by": by}
	for k, val := range extra {
		req[k] = val
	}
	if err := s.writeUpdateJSONAtomic(file, req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write the request: " + err.Error()})
		return
	}
	s.logger.Info("core host action requested", "kind", kind, "by", by)
	req["requested_at"] = at
	writeJSON(w, http.StatusAccepted, req)
}

// handleZbxUpdateNow: POST /api/os/zbx-update, the pending Zabbix minor update, right away.
func (s *Server) handleZbxUpdateNow(w http.ResponseWriter, r *http.Request) {
	v := s.coreOSStatus()
	s.requestHostAction(w, r, "zbx-update", zbxUpdateRequestFile, map[string]any{"from": v.ZbxServer, "to": v.ZbxCandidate})
}

// handleRebootNow: POST /api/os/reboot, the reboot the OS asked for, right away.
func (s *Server) handleRebootNow(w http.ResponseWriter, r *http.Request) {
	s.requestHostAction(w, r, "reboot", rebootRequestFile, nil)
}

// handlePatchNow: POST /api/os/patch, the pending security updates, right away.
func (s *Server) handlePatchNow(w http.ResponseWriter, r *http.Request) {
	s.requestHostAction(w, r, "patch", patchRequestFile, nil)
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
