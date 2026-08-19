package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"argus/internal/buildinfo"
	"argus/internal/config"
)

// newUpdateTestServer builds a minimal Server exercising only the self-update file channel: a shared
// UpdateDir, a resolved "latest", and a stamped running version.
func newUpdateTestServer(t *testing.T, dir, running, latest string) *Server {
	t.Helper()
	s := &Server{
		cfg:       config.Config{UpdateDir: dir},
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		appLatest: &appLatestCache{},
	}
	s.appLatest.set(latest)
	orig := buildinfo.Version
	buildinfo.Version = running
	t.Cleanup(func() { buildinfo.Version = orig })
	return s
}

func readState(t *testing.T, s *Server) updateStateResponse {
	t.Helper()
	st, err := s.currentUpdateState()
	if err != nil {
		t.Fatalf("currentUpdateState: %v", err)
	}
	return st
}

func TestCoreUpdateLifecycle(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateTestServer(t, dir, "v0.4.9", "v0.4.10")

	// Idle to start.
	if st := readState(t, s); st.State != "idle" || !st.SelfUpdateEnabled {
		t.Fatalf("initial state = %+v, want idle + enabled", st)
	}

	// Start an update: an admin trigger drops request.json and the state reads "requested".
	rec := httptest.NewRecorder()
	s.handleUpdateStart(rec, httptest.NewRequest(http.MethodPost, "/api/update/start", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: HTTP %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	st := readState(t, s)
	if st.State != "requested" || st.Target != "v0.4.10" {
		t.Fatalf("after start: %+v, want requested -> v0.4.10", st)
	}

	// A second start while one is in flight is refused.
	rec = httptest.NewRecorder()
	s.handleUpdateStart(rec, httptest.NewRequest(http.MethodPost, "/api/update/start", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second start: HTTP %d, want 409", rec.Code)
	}

	// The sidecar picks it up: read the request id and write a running status with the same id.
	var req updateRequest
	b, _ := os.ReadFile(filepath.Join(dir, updateRequestFile))
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("read request.json: %v", err)
	}
	writeStatus := func(state, msg string) {
		s := coreUpdateStatus{ID: req.ID, State: state, From: req.From, To: req.Tag, Message: msg}
		bb, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(dir, updateStatusFile), bb, 0o644); err != nil {
			t.Fatalf("write status.json: %v", err)
		}
	}

	writeStatus("running", "pulling image")
	if st := readState(t, s); st.State != "running" || st.Message != "pulling image" {
		t.Fatalf("running: %+v", st)
	}

	writeStatus("success", "updated to v0.4.10")
	if st := readState(t, s); st.State != "success" {
		t.Fatalf("success: %+v", st)
	}

	// Dismiss clears the finished job back to idle.
	rec = httptest.NewRecorder()
	s.handleUpdateDismiss(rec, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss: HTTP %d", rec.Code)
	}
	if st := readState(t, s); st.State != "idle" {
		t.Fatalf("after dismiss: %+v, want idle", st)
	}
	if _, err := os.Stat(filepath.Join(dir, updateRequestFile)); !os.IsNotExist(err) {
		t.Fatalf("request.json should be removed after dismiss")
	}
}

func TestCoreUpdateStartRefusedWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateTestServer(t, dir, "v0.4.10", "v0.4.10") // already on the newest release

	rec := httptest.NewRecorder()
	s.handleUpdateStart(rec, httptest.NewRequest(http.MethodPost, "/api/update/start", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start when current: HTTP %d, want 400", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, updateRequestFile)); !os.IsNotExist(err) {
		t.Fatalf("no request.json should be written when no update is available")
	}
}

func TestCoreUpdateDisabledWhenNoDir(t *testing.T) {
	s := newUpdateTestServer(t, "", "v0.4.9", "v0.4.10") // UpdateDir empty -> feature off

	st := readState(t, s)
	if st.SelfUpdateEnabled || st.State != "idle" {
		t.Fatalf("disabled state = %+v, want !enabled + idle", st)
	}
	rec := httptest.NewRecorder()
	s.handleUpdateStart(rec, httptest.NewRequest(http.MethodPost, "/api/update/start", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start when disabled: HTTP %d, want 400", rec.Code)
	}
}
