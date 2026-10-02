// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
)

// probeTargetPin matches an exact immutable probe pin, e.g. "7.0.29-r1" (Zabbix version + our
// wrapper revision). The other accepted target is the rolling tag "latest".
var probeTargetPin = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-r[0-9]+$`)

// validProbeTarget reports whether v is an acceptable fleet target: "latest" or an exact pin.
func validProbeTarget(v string) bool {
	return v == "latest" || probeTargetPin.MatchString(v)
}

// updateStatus classifies a probe's reported version against the fleet target. `latest` is the
// newest version resolved from GHCR (may be "" if not yet known).
//   - unknown : the probe hasn't checked in a version yet (or runs an old, pre-fleet image)
//   - tracking: target is "latest" but GHCR hasn't been resolved yet - drift can't be computed, so
//     the running version is shown for information only
//   - current : the probe is at (or ahead of) the effective target
//   - outdated: the probe is genuinely behind the effective target
//
// For "latest" the test is BEHIND, not just different: the GHCR latest cache refreshes only every few
// hours, so right after a release the fleet can already run the new revision while the cache still
// holds the previous one. A plain equality then flagged the up-to-date probe "outdated -> <older>",
// i.e. it proposed a DOWNGRADE. A probe at or past the resolved latest is current. An explicit pin is
// a deliberate target, so any mismatch (older or newer) is "outdated" - the admin may be pinning back.
func updateStatus(reported, target, latest string) string {
	if reported == "" {
		return "unknown"
	}
	if target == "latest" {
		if latest == "" {
			return "tracking"
		}
		if probeVersionLess(reported, latest) {
			return "outdated"
		}
		return "current"
	}
	if reported == target {
		return "current"
	}
	return "outdated"
}

// probeVersionLess reports whether probe version a is strictly older than b, comparing the
// (major, minor, patch, revision) tuple of an "X.Y.Z-rN" string. Unparseable input compares as
// not-less, so an unrecognised version is never flagged as an outdated downgrade target.
func probeVersionLess(a, b string) bool {
	ma := probeVerTag.FindStringSubmatch(strings.TrimSpace(a))
	mb := probeVerTag.FindStringSubmatch(strings.TrimSpace(b))
	if ma == nil || mb == nil {
		return false
	}
	var ka, kb [4]int
	for i := 0; i < 4; i++ {
		ka[i], _ = strconv.Atoi(ma[i+1])
		kb[i], _ = strconv.Atoi(mb[i+1])
	}
	return versionLess(ka, kb)
}

// handleProbeCheckin is the probe-facing endpoint (public; authenticated by the long-lived probe
// token issued at enrollment). The probe reports its running version and self-updater flag, and
// receives the fleet target version to converge on.
func (s *Server) handleProbeCheckin(w http.ResponseWriter, r *http.Request) {
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
		Version        string `json:"version"`
		SelfUpdate     *bool  `json:"selfupdate"`      // pointer: omitted keeps the stored flag (two-reporter model)
		UpdaterVersion string `json:"updater_version"` // the sidecar reports its own version here
		Scans          *bool  `json:"scans"`           // the proxy container advertises the network-scan capability
		Sweeps         *bool  `json:"sweeps"`          // ... and the UniFi-sweep capability
		// The proxy container reports the Zabbix process counts it started with, and which of them
		// the operator set on the container; the sidecar says whether it can restart the proxy.
		Procs       map[string]int `json:"procs"`
		ProcsPinned []string       `json:"procs_pinned"`
		Restarts    *bool          `json:"restarts"`
		// The proxy container's CPU: how many it sees, a container limit (0 = none) and the load
		// average (1, 5, 15 minutes). The autoscaler tells a probe short on CPU from one short on processes.
		CPU *struct {
			Count int       `json:"count"`
			Quota float64   `json:"quota"`
			Load  []float64 `json:"load"`
		} `json:"cpu"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.st.RecordProbeCheckin(ctx, proxyName, strings.TrimSpace(req.Version), req.SelfUpdate, req.Scans, req.Sweeps); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record check-in"})
		return
	}
	_ = s.st.SetUpdaterVersion(ctx, proxyName, strings.TrimSpace(req.UpdaterVersion))
	if req.Procs != nil {
		running, pinned := cleanProcs(req.Procs, req.ProcsPinned)
		_ = s.st.RecordProbeProcs(ctx, proxyName, running, pinned)
	}
	if req.Restarts != nil {
		_ = s.st.SetProbeRestarts(ctx, proxyName, *req.Restarts)
	}
	if req.CPU != nil {
		if n, q, l, ok := cleanCPU(req.CPU.Count, req.CPU.Quota, req.CPU.Load); ok {
			_ = s.st.RecordProbeCPU(ctx, proxyName, n, q, l[0], l[1], l[2])
		}
	}
	target, _ := s.st.ProbeTargetVersion(ctx)
	var resp struct {
		Target        string           `json:"target"`
		TargetDigest  string           `json:"target_digest,omitempty"` // what the target tag points to right now
		CoreHost      string           `json:"core_host,omitempty"`
		Update        string           `json:"update,omitempty"`
		UpdateDigest  string           `json:"update_digest,omitempty"`
		UpdaterUpdate string           `json:"updater_update,omitempty"`
		UpdaterDigest string           `json:"updater_update_digest,omitempty"`
		Scan          *scanJobPayload  `json:"scan,omitempty"`
		Sweep         *sweepJobPayload `json:"sweep,omitempty"`
		// The process counts the probe should start with (absent: keep what it has), and a one-shot
		// for the sidecar to restart the proxy so it starts with them.
		Procs        map[string]int `json:"procs,omitempty"`
		RestartProxy bool           `json:"restart_proxy,omitempty"`
	}
	resp.Target = target
	s.procsHandout(ctx, proxyName, req.SelfUpdate, req.Restarts, &resp.Procs, &resp.RestartProxy)
	// Only the sidecar acts on tags, and only it gets the digests (a plain reporter needn't cost a
	// registry lookup per minute).
	if req.SelfUpdate != nil && *req.SelfUpdate {
		resp.TargetDigest = s.imageDigest(ctx, probeImageRepo, probeTargetTag(target))
	}
	// Hand out the current core host on every check-in (not gated on self-update capability, so a
	// pure-reporter proxy gets it too). This lets an admin re-point the whole fleet by changing
	// ARGUS_PROBE_CORE_HOST centrally: each probe applies the new value at its next restart. Omitted
	// when unset, so the probe never overwrites its baked value with an empty one.
	resp.CoreHost = s.probeCoreHost()
	// Hand out (and clear) the one-shot updates exactly once - but ONLY to a caller that advertises
	// self-update capability (the socket-holding updater sidecar). Otherwise a socket-less proxy's
	// version-report check-in would consume the one-shot before the sidecar could act, losing it.
	//   update         -> recreate the PROXY onto this tag
	//   updater_update -> the sidecar recreates ITSELF onto this argus-updater tag
	if req.SelfUpdate != nil && *req.SelfUpdate {
		// Each hand-out is remembered, so the system notices can tell when one didn't go through.
		if tag, _ := s.st.TakeProbeUpdate(ctx, proxyName); tag != "" {
			resp.Update = tag
			resp.UpdateDigest = s.imageDigest(ctx, probeImageRepo, probeTargetTag(tag))
			_ = s.st.MetaSet(ctx, noticeProbePending+proxyName, tag+"|"+itoa64(time.Now().Unix()))
		}
		if tag, _ := s.st.TakeUpdaterUpdate(ctx, proxyName); tag != "" {
			resp.UpdaterUpdate = tag
			resp.UpdaterDigest = s.imageDigest(ctx, updaterImageRepo, tag)
			_ = s.st.MetaSet(ctx, noticeUpdPending+proxyName, tag+"|"+itoa64(time.Now().Unix()))
		}
	}
	// Hand out (and mark dispatched) a queued discovery job exactly once - only to the proxy
	// container itself (it advertises the scan capability; the updater sidecar doesn't), same
	// reasoning as the self-update gate above. The handout is shaped by the job's kind (a subnet
	// scan or a UniFi sweep); at most one of the two fields is set. See netdiscovery.go.
	if req.Scans != nil && *req.Scans {
		resp.Scan, resp.Sweep = s.takeDiscoveryHandout(ctx, proxyName)
	}
	// The probe knows its own image repo; it only needs the tag to converge on.
	writeJSON(w, http.StatusOK, resp)
}

// probeTargetTag maps a fleet target to the pullable image tag ("latest" stays "latest"; a pin
// like "7.0.29-r1" maps to itself).
func probeTargetTag(target string) string {
	if target == "" || target == "latest" {
		return "latest"
	}
	return target
}

// handleTriggerProbeUpdate queues a self-update for one probe (admin). The probe must have reported
// it's self-update capable (Docker socket mounted); otherwise the caller should use the manual
// one-click command instead.
func (s *Server) handleTriggerProbeUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	ag, err := s.st.ProbeAgentByName(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus, so it can't be updated from here"})
		return
	}
	if !ag.SelfUpdate {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this probe isn't self-update capable (no Docker socket); use the manual update command"})
		return
	}
	target, _ := s.st.ProbeTargetVersion(ctx)
	tag := probeTargetTag(target)
	if err := s.st.SetProbeUpdate(ctx, name, tag); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not queue the update"})
		return
	}
	_ = s.st.MetaDelete(ctx, noticeProbeFailed+name) // a new try: the old failure no longer shows
	s.logger.Info("probe self-update queued", "proxy", name, "tag", tag)
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "tag": tag})
}

// handleTriggerUpdaterUpdate queues a self-update of the probe's argus-updater sidecar (admin). The
// sidecar recreates itself (via an ephemeral probe-recreate copy) onto the requested argus-updater
// tag - defaulting to the rolling "latest" - at its next check-in. Requires a sidecar to be present
// (the probe advertises self-update capability).
func (s *Server) handleTriggerUpdaterUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	ag, err := s.st.ProbeAgentByName(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
		return
	}
	if !ag.SelfUpdate {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no argus-updater sidecar is managing this probe"})
		return
	}
	// Optional {"tag":"..."} pins a specific argus-updater version; default to the rolling latest.
	tag := "latest"
	var body struct {
		Tag string `json:"tag"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&body) == nil && strings.TrimSpace(body.Tag) != "" {
		tag = strings.TrimSpace(body.Tag)
	}
	if !validImageTag.MatchString(tag) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `tag must be "latest", "testing" or a version like 0.2.5`})
		return
	}
	if err := s.st.SetUpdaterUpdate(ctx, name, tag); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not queue the updater update"})
		return
	}
	_ = s.st.MetaDelete(ctx, noticeUpdFailed+name)
	s.logger.Info("updater self-update queued", "proxy", name, "tag", tag)
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "tag": tag})
}

// validImageTag is the shape of an argus-updater image tag an admin may hand to the sidecars: the
// updater turns it into an image reference and runs it with the Docker socket, so it is a fixed
// vocabulary, not free text.
var validImageTag = regexp.MustCompile(`^(latest|testing|v?[0-9]+\.[0-9]+\.[0-9]+)$`)

// handleIssueCheckinToken mints a check-in credential for a probe that predates the fleet-update
// feature (its enrollment never provisioned one). The admin drops the returned token into the
// container as ARGUS_PROBE_TOKEN - via the Unraid/docker GUI - to turn on version check-in without
// a full re-enrollment. Idempotent: re-issuing rotates the credential.
func (s *Server) handleIssueCheckinToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "proxy name required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	// Only a proxy Zabbix knows gets a credential: a typo would otherwise leave a phantom probe
	// record that can complete discovery jobs.
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not read the proxy list from Zabbix"})
		return
	}
	known := false
	for _, p := range proxies {
		if p.Name == name {
			known = true
			break
		}
	}
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no proxy with that name"})
		return
	}
	raw, hash, err := auth.NewSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.UpsertProbeCredential(ctx, name, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not issue token"})
		return
	}
	s.logger.Info("probe check-in token issued", "proxy", name)
	writeJSON(w, http.StatusOK, map[string]string{"token": raw, "checkin_url": s.probeCheckinURL()})
}

// handleGetProbeTarget returns the fleet's target probe version (admin).
func (s *Server) handleGetProbeTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.st.ProbeTargetVersion(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read target"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"target": target})
}

// handleSetProbeTarget updates the fleet's target probe version (admin). Accepts "latest" or an
// exact pin like "7.0.29-r1".
func (s *Server) handleSetProbeTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	target := strings.TrimSpace(req.Target)
	if !validProbeTarget(target) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `target must be "latest" or a pin like "7.0.29-r1"`})
		return
	}
	if err := s.st.SetProbeTargetVersion(r.Context(), target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save target"})
		return
	}
	s.logger.Info("probe fleet target set", "target", target)
	writeJSON(w, http.StatusOK, map[string]string{"target": target})
}

// bearerToken extracts a Bearer token from the Authorization header ("" if absent).
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
