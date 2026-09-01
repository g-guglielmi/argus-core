package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
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
//   - current : reported version equals the effective target (the pin, or the resolved newest)
//   - outdated: reported version differs from the effective target
func updateStatus(reported, target, latest string) string {
	if reported == "" {
		return "unknown"
	}
	want := target
	if target == "latest" {
		if latest == "" {
			return "tracking"
		}
		want = latest
	}
	if reported == want {
		return "current"
	}
	return "outdated"
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
		Version    string `json:"version"`
		SelfUpdate *bool  `json:"selfupdate"` // pointer: omitted keeps the stored flag (two-reporter model)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.st.RecordProbeCheckin(ctx, proxyName, strings.TrimSpace(req.Version), req.SelfUpdate); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record check-in"})
		return
	}
	target, _ := s.st.ProbeTargetVersion(ctx)
	resp := map[string]string{"target": target}
	// Hand out (and clear) a dashboard-requested self-update exactly once - but ONLY to a caller that
	// advertises self-update capability. Otherwise a socket-less proxy's version-report check-in would
	// consume the one-shot before the socket-holding updater sidecar (which polls the same token)
	// could act on it, and the update would be silently lost.
	if req.SelfUpdate != nil && *req.SelfUpdate {
		if tag, _ := s.st.TakeProbeUpdate(ctx, proxyName); tag != "" {
			resp["update"] = tag
		}
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
	s.logger.Info("probe self-update queued", "proxy", name, "tag", tag)
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "tag": tag})
}

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
	writeJSON(w, http.StatusOK, map[string]string{"token": raw, "checkin_url": s.baseURL(r) + "/api/probes/checkin"})
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
