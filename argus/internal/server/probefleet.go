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

// updateStatus classifies a probe's reported version against the fleet target.
//   - unknown : the probe hasn't checked in a version yet (or runs an old, pre-fleet image)
//   - tracking: target is "latest" - drift can't be computed centrally (the probe/updater
//     converges on the newest digest), so the running version is shown for information only
//   - current : reported version equals the pinned target
//   - outdated: reported version differs from the pinned target
func updateStatus(reported, target string) string {
	if reported == "" {
		return "unknown"
	}
	if target == "latest" {
		return "tracking"
	}
	if reported == target {
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
		SelfUpdate bool   `json:"selfupdate"`
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
	// Hand out a dashboard-requested self-update exactly once. The probe (with the Docker socket)
	// converges by spawning a short-lived recreate helper; empty means nothing queued.
	if tag, _ := s.st.TakeProbeUpdate(ctx, proxyName); tag != "" {
		resp["update"] = tag
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
