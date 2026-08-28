package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/buildinfo"
)

// One-click core self-update: a file-drop channel between the public-facing Argus core and the
// argus-updater sidecar (which alone holds the Docker socket). The two share a small volume
// (ARGUS_UPDATE_DIR, e.g. /update); the core never touches Docker.
//
//	request.json  - written by the core when an admin clicks "Update now" (the command)
//	status.json   - written by the sidecar as it works: running -> success | failed (the result)
//
// The sidecar pulls the target image, recreates the core cloning its config, verifies the new
// container stays healthy, and rolls back on failure - reporting the outcome (and reason) back in
// status.json so the core can show a banner. Writes are atomic (tmp + rename) so neither side ever
// reads a half-written file; both files carry the same `id` so a stale status is ignored.

const (
	updateRequestFile = "request.json"
	updateStatusFile  = "status.json"
)

// updateRequest is the command the core drops for the sidecar.
type updateRequest struct {
	ID            string `json:"id"`
	Tag           string `json:"tag"`             // image tag to converge on, e.g. "v0.5.0"
	Exact         bool   `json:"exact,omitempty"` // deliberate channel/version switch: use Tag verbatim (bypass channel-preserve)
	From          string `json:"from"`            // running version at request time
	RequestedBy   string `json:"requested_by"`    // admin email, for the audit line
	RequestedAt   string `json:"requested_at"`    // RFC3339
	CoreContainer string `json:"core_container"`  // hostname hint (= container id by default)
}

// coreUpdateStatus is the sidecar's report, overwritten in place through the job's lifecycle.
type coreUpdateStatus struct {
	ID         string `json:"id"`
	State      string `json:"state"` // running | success | failed
	From       string `json:"from"`
	To         string `json:"to"`
	Message    string `json:"message"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// updateStateResponse drives the Settings UI: the button, the poll, and the banner.
type updateStateResponse struct {
	SelfUpdateEnabled bool   `json:"self_update_enabled"`
	State             string `json:"state"` // idle | requested | running | success | failed
	Target            string `json:"target,omitempty"`
	From              string `json:"from,omitempty"`
	Message           string `json:"message,omitempty"`
	RequestedBy       string `json:"requested_by,omitempty"`
	StartedAt         string `json:"started_at,omitempty"`
	FinishedAt        string `json:"finished_at,omitempty"`
}

func (s *Server) updatePath(name string) string { return filepath.Join(s.cfg.UpdateDir, name) }

// readUpdateJSON loads a channel file into out. Returns (false, nil) when the file is absent.
func readUpdateJSON(path string, out any) (bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false, err
	}
	return true, nil
}

// writeUpdateJSONAtomic writes v as JSON via a temp file + rename, so a reader never sees a partial
// write. The dir is created if missing (the shared volume may mount empty).
func (s *Server) writeUpdateJSONAtomic(name string, v any) error {
	if err := os.MkdirAll(s.cfg.UpdateDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	final := s.updatePath(name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// currentUpdateState reads the channel files and derives the state the UI should render.
func (s *Server) currentUpdateState() (updateStateResponse, error) {
	resp := updateStateResponse{SelfUpdateEnabled: s.cfg.SelfUpdateEnabled(), State: "idle"}
	if !s.cfg.SelfUpdateEnabled() {
		return resp, nil
	}
	var req updateRequest
	hasReq, err := readUpdateJSON(s.updatePath(updateRequestFile), &req)
	if err != nil {
		return resp, err
	}
	if !hasReq {
		return resp, nil
	}
	resp.Target, resp.From, resp.RequestedBy = req.Tag, req.From, req.RequestedBy

	var st coreUpdateStatus
	hasStatus, err := readUpdateJSON(s.updatePath(updateStatusFile), &st)
	if err != nil {
		return resp, err
	}
	if hasStatus && st.ID == req.ID {
		resp.State, resp.Message, resp.StartedAt, resp.FinishedAt = st.State, st.Message, st.StartedAt, st.FinishedAt
		if st.To != "" {
			resp.Target = st.To
		}
		return resp, nil
	}
	// Request written, but the sidecar hasn't claimed it yet (or the status is stale from a prior job).
	resp.State = "requested"
	return resp, nil
}

func newUpdateID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// handleUpdateStart queues a core self-update (admin). Requires the argus-updater channel to be wired
// (ARGUS_UPDATE_DIR set) and a newer release to actually be available; refuses if a job is already in
// flight. Drops request.json for the sidecar and clears any stale status.
func (s *Server) handleUpdateStart(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SelfUpdateEnabled() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "self-update is not enabled (no argus-updater sidecar configured); update manually"})
		return
	}
	// An optional {target} body means a deliberate channel/version switch (latest / testing / a
	// specific vX.Y.Z). Without it, this is a plain in-place update on the current channel.
	var body struct {
		Target string `json:"target"`
	}
	decodeOptional(w, r, &body)
	body.Target = strings.TrimSpace(body.Target)

	cur := buildinfo.Version
	latest := s.appLatest.get()
	status, updateAvailable := appUpdateStatus(cur, latest)
	devUpd, _ := s.appLatest.getDevUpdate()
	devUpdate := status == "development" && devUpd

	var targetTag string
	var exact bool
	if body.Target != "" {
		// Deliberate switch: honour the exact tag (bypassing the updater's channel-preserve), but only
		// if it's one of the offered targets - never an arbitrary tag.
		if !s.isAllowedTarget(body.Target) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown update target"})
			return
		}
		targetTag, exact = body.Target, true
	} else {
		// Plain in-place update: a :testing build isn't "outdated" against releases but may have a
		// newer testing image; the target is the channel word and the sidecar re-pulls it in place.
		if !updateAvailable && !devUpdate {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no newer release is available to update to", "status": status})
			return
		}
		targetTag = latest
		if devUpdate {
			targetTag = "testing"
		}
	}

	state, err := s.currentUpdateState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read update state"})
		return
	}
	if state.State == "requested" || state.State == "running" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already in progress"})
		return
	}

	by := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		by = u.Email
	}
	host, _ := os.Hostname()
	req := updateRequest{
		ID:            newUpdateID(),
		Tag:           targetTag,
		Exact:         exact,
		From:          cur,
		RequestedBy:   by,
		RequestedAt:   time.Now().UTC().Format(time.RFC3339),
		CoreContainer: host,
	}
	// Clear any prior status so the state reads cleanly as "requested" until the sidecar reports.
	_ = os.Remove(s.updatePath(updateStatusFile))
	if err := s.writeUpdateJSONAtomic(updateRequestFile, req); err != nil {
		s.logger.Error("core self-update: could not write request", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not queue the update"})
		return
	}
	// Record the channel a deliberate switch selects, so a later clean-aligned build (identical on
	// :latest and :testing) still knows which channel it tracks. A specific vX.Y.Z pins to the release
	// line, which the update check treats as :latest.
	if body.Target != "" {
		ch := "latest"
		if body.Target == "testing" {
			ch = "testing"
		}
		_ = s.st.MetaSet(r.Context(), updateChannelKey, ch)
	}
	s.logger.Info("core self-update queued", "tag", req.Tag, "from", req.From, "by", by)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "tag": req.Tag})
}

// handleUpdateState returns the current self-update state so the UI can drive the button and banner.
func (s *Server) handleUpdateState(w http.ResponseWriter, _ *http.Request) {
	state, err := s.currentUpdateState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read update state"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleUpdateDismiss clears a finished (success/failed) update so its banner goes away (admin).
func (s *Server) handleUpdateDismiss(w http.ResponseWriter, _ *http.Request) {
	if !s.cfg.SelfUpdateEnabled() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	state, err := s.currentUpdateState()
	if err == nil && (state.State == "requested" || state.State == "running") {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is in progress; cannot dismiss yet"})
		return
	}
	_ = os.Remove(s.updatePath(updateRequestFile))
	_ = os.Remove(s.updatePath(updateStatusFile))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
