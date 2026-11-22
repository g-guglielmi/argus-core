// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"argus/internal/notify"
	"argus/internal/syslogout"
	"argus/internal/testrestore"
)

// asTestRestore makes this core a test restore for the test, the way argus-restore does: the mark in the
// data folder.
func asTestRestore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, testrestore.FileName), []byte(`{"since":1700000000,"archive":"argus-backup-core-20261121T100000Z.tar.gpg","from":"core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	testrestore.Init(dir)
	t.Cleanup(func() { testrestore.Init("") })
}

// A test restore has the real core's backup target in its settings, and must leave it alone: the plan the
// host gets has no target, and there is nothing to check.
func TestTestRestoreExportsNothing(t *testing.T) {
	s, dir := newBackupServer(t)
	body, _ := json.Marshal(map[string]any{
		"config":       backupConfig{Enabled: true, Hour: 3, Keep: 5, History: true, Remote: backupRemote{Type: "smb", Share: "//nas.example.lan/backups", Username: "argus"}},
		"passphrase":   "correct horse battery",
		"smb_password": "s3cret-pass",
	})
	rec := httptest.NewRecorder()
	s.handleSetBackup(rec, httptest.NewRequest(http.MethodPut, "/api/backup", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if p := LoadBackupPlan(t.Context(), s.st, "UTC"); p.Remote.Type != "smb" || p.Password == "" {
		t.Fatalf("the real core exports: %+v", p)
	}
	asTestRestore(t)
	p := LoadBackupPlan(t.Context(), s.st, "UTC")
	if p.Remote.Type != "" || p.Remote.Share != "" || p.Password != "" {
		t.Fatalf("a test restore's plan still has a target: %+v", p)
	}
	if !p.Enabled || p.Passphrase != "correct horse battery" || p.Keep != 5 {
		t.Fatalf("the rest of the plan came back with the restore and stays: %+v", p)
	}
	run := func(kind string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleBackupRun(rec, httptest.NewRequest(http.MethodPost, "/api/backup/run", strings.NewReader(`{"kind":"`+kind+`"}`)))
		return rec
	}
	if rec := run("test"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "test restore") {
		t.Fatalf("checking the target on a test restore: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, backupRequestFile)); err == nil {
		t.Fatal("the refused check still reached the host")
	}
	if rec := run("backup"); rec.Code != 200 {
		t.Fatalf("a backup on the test restore itself is fine: %d %s", rec.Code, rec.Body)
	}
}

// A test restore sends nothing: no alert or test through a channel, no mail, no syslog line.
func TestTestRestoreSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	asTestRestore(t)
	ch := notify.Channel{Type: "webhook", Name: "ops", Enabled: true, Config: map[string]string{"url": srv.URL}}
	if err := notify.Send(t.Context(), ch, notify.Event{Kind: "test"}); err != testrestore.ErrNoSend {
		t.Fatalf("a channel send on a test restore: %v", err)
	}
	if err := (notify.SMTP{Host: "127.0.0.1", Port: "1", From: "argus@example.com"}).Send(t.Context(), []string{"alerts@example.com"}, []byte("x")); err != testrestore.ErrNoSend {
		t.Fatalf("a mail on a test restore: %v", err)
	}
	if hits != 0 {
		t.Fatalf("%d request(s) left the test restore", hits)
	}
	f := syslogout.New(func() syslogout.Config { return syslogout.Config{Server: "127.0.0.1:1", Protocol: "udp"} })
	f.Send(syslogout.Entry{Message: "m"})
	if q := f.Status().Queued; q != 0 {
		t.Fatalf("a test restore queued %d syslog line(s)", q)
	}
	if err := f.Test(t.Context(), syslogout.Entry{Message: "m"}); err != testrestore.ErrNoSend {
		t.Fatalf("a syslog test on a test restore: %v", err)
	}
}

// A test restore must not vouch for the real core: the heartbeat holds, and says why.
func TestTestRestoreHoldsTheHeartbeat(t *testing.T) {
	s, _ := newBackupServer(t)
	asTestRestore(t)
	st := s.beat(t.Context())
	if st.Held != "this core is a test restore" || st.OKAt != 0 {
		t.Fatalf("heartbeat on a test restore: %+v", st)
	}
}

// Every page learns it from the version answer.
func TestTestRestoreShowsInVersion(t *testing.T) {
	s, _ := newBackupServer(t)
	s.appLatest = &appLatestCache{}
	get := func() versionResponse {
		rec := httptest.NewRecorder()
		s.handleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
		var v versionResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v
	}
	if v := get(); v.TestRestore != nil {
		t.Fatalf("a real core is no test restore: %+v", v.TestRestore)
	}
	asTestRestore(t)
	if v := get(); v.TestRestore == nil || v.TestRestore.From != "core" || v.TestRestore.Since != 1700000000 {
		t.Fatalf("the test restore is not in the version answer: %+v", v.TestRestore)
	}
}
