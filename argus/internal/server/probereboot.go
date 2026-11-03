// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// Probe VM reboots (DESIGN 14c). A probe VM patches itself like the core does (security updates
// only), and reboots only when an update needs it, in its reboot window: the fleet's, or one of its
// own. The windows read on the core's clock, whatever time zone a VM runs, so 03:00 is one moment at
// every site. Reboot now and Patch now work as they do for the core, by a request file the host acts
// on, only here the file reaches the VM through the probe's minute check-in: the proxy container
// writes what Argus hands it into its data folder, and a path unit on the VM starts the host script.
// Argus never reboots or patches anything itself.

// probeRebootWindowKey is the app_meta key holding the fleet's probe reboot window (JSON).
const probeRebootWindowKey = "probe_reboot_window"

// everyDay is a probe window's weekday when it applies every day.
const everyDay = -1

// probeReqTTL is how long a Reboot now or Patch now waits for the VM to settle it: a reboot is a
// minute or two and the first report after boot another, so one still open after this never got there.
const probeReqTTL = 20 * 60

// probeFastCheckin is how soon a probe and its sidecar check in again while the Updates page is
// open, so a click there is taken within seconds rather than at the next minute.
const probeFastCheckin = 15

// defaultProbeWindow is what the probe VMs did before the window existed: every day at 03:00 when an
// update needs a reboot.
func defaultProbeWindow() rebootWindow { return rebootWindow{Mode: "auto", Weekday: everyDay, Hour: 3} }

// normal drops the day and time of a notify-only window, so two equal policies compare equal.
func (rw rebootWindow) normal() rebootWindow {
	if rw.Mode != "auto" {
		return rebootWindow{Mode: "notify"}
	}
	return rw
}

// loadProbeWindow reads the fleet's probe reboot window (the default when unset or unparseable).
func (s *Server) loadProbeWindow(ctx context.Context) rebootWindow {
	raw, ok, err := s.st.MetaGet(ctx, probeRebootWindowKey)
	if err != nil || !ok || raw == "" {
		return defaultProbeWindow()
	}
	var rw rebootWindow
	if json.Unmarshal([]byte(raw), &rw) != nil || !rw.valid() {
		return defaultProbeWindow()
	}
	return rw.normal()
}

// ownProbeWindow is a probe's own window, nil when it follows the fleet's.
func ownProbeWindow(doc string) *rebootWindow {
	if doc == "" {
		return nil
	}
	var rw rebootWindow
	if json.Unmarshal([]byte(doc), &rw) != nil || !rw.valid() {
		return nil
	}
	rw = rw.normal()
	return &rw
}

// probeWindowFile is the window as the VM's reboot watcher reads it (reboot-window.json): the policy
// and the zone of the core's clock ("" when unknown: the VM then uses its own).
type probeWindowFile struct {
	rebootWindow
	TZ string `json:"tz,omitempty"`
}

// coreClockTZ is the core's time zone: the one set in Settings, else the one the core VM reports.
func (s *Server) coreClockTZ() string {
	if tz, configured := s.mgr.ConfiguredTimezone(); configured && validTimezone(tz) {
		return tz
	}
	if tz := s.coreOSStatus().TZ; validTimezone(tz) {
		return tz
	}
	return ""
}

// probeHostReq is a Reboot now or Patch now an admin asked for: when, by whom, and when the probe's
// check-in took it (0 = not yet).
type probeHostReq struct {
	Kind string `json:"kind"` // reboot | patch
	At   int64  `json:"at"`
	By   string `json:"by,omitempty"`
	Sent int64  `json:"sent,omitempty"`
}

func parseProbeHostReq(doc string, now int64) *probeHostReq {
	if doc == "" {
		return nil
	}
	var q probeHostReq
	if json.Unmarshal([]byte(doc), &q) != nil || (q.Kind != "reboot" && q.Kind != "patch") || now-q.At > probeReqTTL {
		return nil
	}
	return &q
}

// probeHostActions is the VM's host reporter on Reboot now and Patch now: whether its scripts take
// them, when the VM booted, and the last result of each (the files the scripts leave behind).
type probeHostActions struct {
	RebootNow bool              `json:"reboot_now"`
	PatchNow  bool              `json:"patch_now"`
	BootedAt  int64             `json:"booted_at,omitempty"`
	Reboot    *hostActionResult `json:"reboot,omitempty"`
	Patch     *hostActionResult `json:"patch,omitempty"`
}

func parseProbeHostActions(doc string) probeHostActions {
	var a probeHostActions
	if doc != "" {
		_ = json.Unmarshal([]byte(doc), &a)
	}
	return a
}

// cleanHostResult bounds a result a VM reported.
func cleanHostResult(r *hostActionResult) *hostActionResult {
	if r == nil || r.State == "" {
		return nil
	}
	switch r.State {
	case "done", "failed", "rebooting":
	default:
		return nil
	}
	why := r.Why
	if why != "request" && why != "window" {
		why = ""
	}
	return &hostActionResult{State: r.State, At: r.At, Why: why, Detail: clip(printable(r.Detail), 240)}
}

// settledBy reports whether the VM's latest report closes the request: the matching script's result
// came after it, or, for a reboot, the VM booted after it.
func (q *probeHostReq) settledBy(a probeHostActions) bool {
	switch q.Kind {
	case "reboot":
		return a.BootedAt >= q.At || (a.Reboot != nil && a.Reboot.At >= q.At && a.Reboot.State != "rebooting")
	case "patch":
		return a.Patch != nil && a.Patch.At >= q.At
	}
	return true
}

// recordProbeHostActions stores what a VM's report says about its reboots, and closes the request it
// settles. Called from the OS status report.
func (s *Server) recordProbeHostActions(ctx context.Context, name string, a probeHostActions) {
	a.Reboot, a.Patch = cleanHostResult(a.Reboot), cleanHostResult(a.Patch)
	b, _ := json.Marshal(a)
	_ = s.st.SetProbeHostActions(ctx, name, string(b))
	h, err := s.st.ProbeHostByName(ctx, name)
	if err != nil || h.Req == "" {
		return
	}
	if q := parseProbeHostReq(h.Req, time.Now().Unix()); q == nil || q.settledBy(a) {
		_, _ = s.st.SwapProbeHostReq(ctx, name, h.Req, "")
	}
}

// probeHostHandout is what a probe VM's proxy container gets at check-in: the window to keep in its
// data folder, and a request not handed out yet (taken once).
func (s *Server) probeHostHandout(ctx context.Context, name string, ag *store.ProbeAgent) (*probeWindowFile, *probeHostReq) {
	if ag == nil || ag.OSReportedAt == 0 { // not a probe VM: nothing on the host to tell
		return nil, nil
	}
	h, err := s.st.ProbeHostByName(ctx, name)
	if err != nil {
		return nil, nil
	}
	w := s.loadProbeWindow(ctx)
	if own := ownProbeWindow(h.Window); own != nil {
		w = *own
	}
	win := &probeWindowFile{rebootWindow: w, TZ: s.coreClockTZ()}
	now := time.Now().Unix()
	q := parseProbeHostReq(h.Req, now)
	if q == nil || q.Sent != 0 {
		return win, nil
	}
	sent := *q
	sent.Sent = now
	b, _ := json.Marshal(sent)
	if ok, _ := s.st.SwapProbeHostReq(ctx, name, h.Req, string(b)); !ok {
		return win, nil
	}
	s.logger.Info("probe host action handed out", "proxy", name, "kind", q.Kind)
	return win, &probeHostReq{Kind: q.Kind, At: q.At}
}

// probesFast reports whether the probes should check in fast now: someone has the Updates page open,
// or Argus has just started.
func (s *Server) probesFast() bool { return time.Now().Unix() < s.updatesWatched.Load() }

// probesWarmFor is how long one ping of the Updates page (it sends one every 90 seconds) or a start of
// Argus keeps the fleet checking in fast.
const probesWarmFor = 2 * time.Minute

// POST /api/probes/warm (admin): the Updates page is open, so the fleet checks in fast for a while
// and a Reboot now, a Patch now or an Update there is taken within seconds.
func (s *Server) handleWarmProbes(w http.ResponseWriter, r *http.Request) {
	s.updatesWatched.Store(time.Now().Add(probesWarmFor).Unix())
	w.WriteHeader(http.StatusNoContent)
}

// probeHostView is a probe VM's reboots as the Updates and Probes pages show them.
type probeHostView struct {
	OwnWindow *rebootWindow     `json:"own_window,omitempty"` // nil: the fleet's
	Now       bool              `json:"now,omitempty"`        // the VM takes Reboot now and Patch now
	Pending   *probeHostReq     `json:"pending,omitempty"`    // a request on its way, with when the check-in took it
	Reboot    *hostActionResult `json:"reboot,omitempty"`     // the last result of each
	Patch     *hostActionResult `json:"patch,omitempty"`
}

func probeHostViewOf(h store.ProbeHost, now int64) *probeHostView {
	a := parseProbeHostActions(h.Actions)
	v := &probeHostView{OwnWindow: ownProbeWindow(h.Window), Now: a.RebootNow && a.PatchNow, Pending: parseProbeHostReq(h.Req, now), Reboot: a.Reboot, Patch: a.Patch}
	if v.Pending != nil {
		v.Pending.By = ""
	}
	return v
}

// probeRequestRefusal says why a probe's Reboot now or Patch now can't go ahead, "" when it can.
func probeRequestRefusal(kind string, ag *store.ProbeAgent, h store.ProbeHost, now int64) string {
	if ag.OSReportedAt == 0 {
		return "this probe is not a probe VM: its operating system is its Docker host's"
	}
	a := parseProbeHostActions(h.Actions)
	if !a.RebootNow || !a.PatchNow {
		return "this probe VM's host tools predate Reboot now and Patch now: they come with its next probe update"
	}
	if q := parseProbeHostReq(h.Req, now); q != nil {
		if q.Kind == "reboot" {
			return "a reboot is already requested"
		}
		return "patching is already requested"
	}
	switch kind {
	case "reboot":
		if !ag.RebootRequired {
			return "the probe VM does not need a reboot"
		}
	case "patch":
		if ag.SecUpdates <= 0 {
			return "no security updates are pending"
		}
	}
	return ""
}

// requestProbeHostAction queues a Reboot now or Patch now for a probe VM (admin): its next check-in
// hands it to the proxy container, which drops the request file for the host.
func (s *Server) requestProbeHostAction(w http.ResponseWriter, r *http.Request, kind string) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	ag, err := s.st.ProbeAgentByName(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
		return
	}
	h, err := s.st.ProbeHostByName(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
		return
	}
	now := time.Now().Unix()
	if why := probeRequestRefusal(kind, ag, h, now); why != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": why})
		return
	}
	by := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		by = u.Email
	}
	b, _ := json.Marshal(probeHostReq{Kind: kind, At: now, By: by})
	if ok, err := s.st.SwapProbeHostReq(ctx, name, h.Req, string(b)); err != nil || !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "another request changed it meanwhile: try again"})
		return
	}
	s.logger.Info("probe host action requested", "proxy", name, "kind", kind, "by", by)
	writeJSON(w, http.StatusAccepted, map[string]any{"kind": kind, "at": now})
}

// POST /api/probes/{name}/reboot: the reboot the probe VM's OS asked for, right away.
func (s *Server) handleProbeRebootNow(w http.ResponseWriter, r *http.Request) {
	s.requestProbeHostAction(w, r, "reboot")
}

// POST /api/probes/{name}/patch: the probe VM's pending security updates, right away.
func (s *Server) handleProbePatchNow(w http.ResponseWriter, r *http.Request) {
	s.requestProbeHostAction(w, r, "patch")
}

// decodeProbeWindow reads a probe window from a request body.
func decodeProbeWindow(w http.ResponseWriter, r *http.Request) (rebootWindow, bool) {
	var rw rebootWindow
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&rw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return rw, false
	}
	if !rw.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "notify" or "auto"; for auto, weekday -1 (every day) to 6, hour 0-23, minute 0-59`})
		return rw, false
	}
	return rw.normal(), true
}

// PUT /api/os/probe-reboot-window (admin): the fleet's probe reboot window.
func (s *Server) handleSetProbeWindow(w http.ResponseWriter, r *http.Request) {
	rw, ok := decodeProbeWindow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b, _ := json.Marshal(rw)
	if err := s.st.MetaSet(ctx, probeRebootWindowKey, string(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the reboot window"})
		return
	}
	s.logger.Info("probe reboot window set", "mode", rw.Mode, "weekday", rw.Weekday, "hour", rw.Hour, "minute", rw.Minute)
	writeJSON(w, http.StatusOK, rw)
}

// PUT /api/probes/{name}/reboot-window (admin): a probe's own window, or {"fleet":true} to put it back
// on the fleet's. A window equal to the fleet's is stored as its own all the same: it stays when the
// fleet's changes.
func (s *Server) handleSetOwnProbeWindow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Fleet bool `json:"fleet"`
		rebootWindow
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	doc := ""
	if !body.Fleet {
		if !body.rebootWindow.valid() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "notify" or "auto"; for auto, weekday -1 (every day) to 6, hour 0-23, minute 0-59`})
			return
		}
		b, _ := json.Marshal(body.rebootWindow.normal())
		doc = string(b)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.st.SetProbeRebootWindow(ctx, name, doc); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the reboot window"})
		return
	}
	s.logger.Info("probe own reboot window set", "proxy", name, "fleet", body.Fleet)
	writeJSON(w, http.StatusOK, map[string]any{"own_window": ownProbeWindow(doc)})
}
