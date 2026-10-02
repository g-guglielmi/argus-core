// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A sidecar update shows where it has got to: queued, the sidecar's own steps, or (for a sidecar
// that reports none) its new version; a finished one goes after half an hour, a failed one stays.
func TestSidecarJobFrom(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	job := sidecarJobFile{ID: "j1", Tag: "latest", From: "v0.2.11", RequestedBy: "admin@example.com", RequestedAt: at(time.Minute)}

	if got := sidecarJobFrom(job, true, nil, "v0.2.11", now); got.State != "queued" || len(got.Steps) != 1 {
		t.Fatalf("queued: %+v", got)
	}
	st := &sidecarStatus{ID: "j1", State: "running", Steps: []jobStep{{Msg: "picked up"}, {Msg: "pulling"}}}
	if got := sidecarJobFrom(job, false, st, "v0.2.11", now); got.State != "running" || len(got.Steps) != 3 || got.Steps[2].Msg != "pulling" {
		t.Fatalf("the sidecar's steps: %+v", got)
	}
	other := &sidecarStatus{ID: "old", State: "failed"}
	if got := sidecarJobFrom(job, false, other, "v0.2.12", now); got.State != "success" || got.To != "v0.2.12" {
		t.Fatalf("a sidecar that reports nothing, on a new version: %+v", got)
	}
	if got := sidecarJobFrom(job, false, nil, "v0.2.11", now); got.State != "running" {
		t.Fatalf("no word yet, within 3 minutes: %+v", got)
	}
	late := job
	late.RequestedAt = at(5 * time.Minute)
	if got := sidecarJobFrom(late, false, nil, "v0.2.11", now); got.State != "unknown" || !strings.Contains(got.Message, "v0.2.11") {
		t.Fatalf("no word after 3 minutes: %+v", got)
	}
	done := &sidecarStatus{ID: "j1", State: "success", FinishedAt: at(40 * time.Minute)}
	if got := sidecarJobFrom(job, false, done, "v0.2.12", now); got != nil {
		t.Fatalf("a success from 40 minutes ago is still shown: %+v", got)
	}
	failed := &sidecarStatus{ID: "j1", State: "failed", Message: "rolled back", FinishedAt: at(2 * time.Hour)}
	if got := sidecarJobFrom(job, false, failed, "v0.2.11", now); got == nil || got.State != "failed" {
		t.Fatalf("a failure waits to be closed: %+v", got)
	}
}
