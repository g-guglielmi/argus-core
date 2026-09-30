// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"argus/internal/config"
	"argus/internal/store"
)

func TestBackupConfigValidate(t *testing.T) {
	ok := func(r backupRemote) backupConfig { return backupConfig{Hour: 2, Minute: 30, Keep: 7, Remote: r} }
	for _, c := range []struct {
		cfg  backupConfig
		want string
	}{
		{ok(backupRemote{}), ""},
		{ok(backupRemote{Type: "smb", Share: `\\nas.example.lan\backups`, Username: "argus", Path: "core/"}), ""},
		{ok(backupRemote{Type: "smb", Share: "//nas/backups", Username: "a;b"}), "the user name and domain may hold letters, digits and . _ @ - only"},
		{ok(backupRemote{Type: "smb", Share: "nas/backups"}), "the share must look like //server/share"},
		{ok(backupRemote{Type: "smb", Share: "//nas/backups", Path: "../etc"}), "the folder must be a plain path inside the target (letters, digits, . _ - and spaces)"},
		{ok(backupRemote{Type: "nfs", Export: "nas.example.lan:/volume1/backups", Options: "vers=4.1"}), ""},
		{ok(backupRemote{Type: "nfs", Export: "nas:/x", Options: "vers=4;rm"}), "mount options may hold letters, digits and = , . _ - only"},
		{ok(backupRemote{Type: "rsync", Target: "backup@nas.example.lan:/volume1/backups/argus"}), ""},
		{ok(backupRemote{Type: "rsync", Target: "-oProxyCommand=x@h:/p"}), "the target must look like user@server:/path"},
		{ok(backupRemote{Type: "s3", Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "argus-backups", AccessKey: "AKIAEXAMPLE"}), ""},
		{ok(backupRemote{Type: "s3", Endpoint: "ftp://x", Bucket: "argus-backups", AccessKey: "A"}), "the endpoint must be an http(s) address, like https://s3.eu-central-1.amazonaws.com"},
		{ok(backupRemote{Type: "s3", Endpoint: "https://x", Bucket: "A_B", AccessKey: "A"}), "the bucket name must be 3-63 lowercase letters, digits, dots or dashes"},
		{ok(backupRemote{Type: "ftp"}), "unknown remote type"},
		{backupConfig{Hour: 24, Keep: 7}, "the backup time must be a time of day"},
		{backupConfig{Hour: 1, Keep: 0}, "keep between 1 and 90 backups"},
	} {
		cfg := c.cfg
		if got := cfg.validate(); got != c.want {
			t.Errorf("validate(%+v) = %q, want %q", c.cfg.Remote, got, c.want)
		}
	}
	c := ok(backupRemote{Type: "smb", Share: `\\nas\backups`, Path: "/core/", Target: "stale"})
	_ = c.validate()
	if c.Remote.Share != "//nas/backups" || c.Remote.Path != "core" || c.Remote.Target != "" {
		t.Fatalf("normalized remote = %+v", c.Remote)
	}
	r := ok(backupRemote{Type: "rsync", Target: "u@h:/p"})
	_ = r.validate()
	if r.Remote.Port != 22 {
		t.Fatalf("rsync port defaults to 22, got %d", r.Remote.Port)
	}
}

func newBackupServer(t *testing.T) (*Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	return &Server{st: st, cfg: config.Config{UpdateDir: dir}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, dir
}

// Saving: a remote needs a passphrase; secrets never come back; the plan hands the host everything.
func TestBackupSaveAndPlan(t *testing.T) {
	s, dir := newBackupServer(t)
	put := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		s.handleSetBackup(rec, httptest.NewRequest(http.MethodPut, "/api/backup", bytes.NewReader(b)))
		return rec
	}
	smb := backupConfig{Enabled: true, Hour: 3, Keep: 5, History: true, Remote: backupRemote{Type: "smb", Share: "//nas.example.lan/backups", Username: "argus"}}
	if rec := put(map[string]any{"config": smb}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "passphrase") {
		t.Fatalf("a remote without a passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := put(map[string]any{"config": smb, "passphrase": "short"}); rec.Code != 400 {
		t.Fatalf("a short passphrase: %d", rec.Code)
	}
	rec := put(map[string]any{"config": smb, "passphrase": "correct horse battery", "smb_password": "s3cret-pass"})
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cret-pass") || strings.Contains(rec.Body.String(), "correct horse") {
		t.Fatal("a secret came back to the browser")
	}
	var v backupView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.HasPassphrase || !v.HasSMBPassword || v.Config.Keep != 5 {
		t.Fatalf("view = %+v", v)
	}
	// blank secrets on a later save leave them as they are
	smb.Keep = 6
	if rec := put(map[string]any{"config": smb}); rec.Code != 200 {
		t.Fatalf("resave: %d %s", rec.Code, rec.Body)
	}
	p := LoadBackupPlan(t.Context(), s.st, "Europe/Rome")
	if p.Passphrase != "correct horse battery" || p.Password != "s3cret-pass" || p.Keep != 6 || p.Timezone != "Europe/Rome" || p.Remote.Share != "//nas.example.lan/backups" {
		t.Fatalf("plan = %+v", p)
	}
	// rsync: a key pair appears, and the plan carries the private half
	rs := backupConfig{Enabled: true, Hour: 3, Keep: 5, Remote: backupRemote{Type: "rsync", Target: "backup@nas.example.lan:/backups/argus"}}
	rec = put(map[string]any{"config": rs})
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || !strings.HasPrefix(v.SSHPublicKey, "ssh-ed25519 ") {
		t.Fatalf("rsync save: %d %s", rec.Code, rec.Body)
	}
	p = LoadBackupPlan(t.Context(), s.st, "UTC")
	if _, err := ssh.ParsePrivateKey([]byte(p.PrivateKey)); err != nil || p.Password != "" {
		t.Fatalf("the plan's private key doesn't parse (%v), or the SMB password leaked into an rsync plan", err)
	}
	// Back up now drops a request for the host
	run := httptest.NewRecorder()
	s.handleBackupRun(run, httptest.NewRequest(http.MethodPost, "/api/backup/run", strings.NewReader(`{"kind":"backup"}`)))
	if run.Code != 200 {
		t.Fatalf("run: %d %s", run.Code, run.Body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, backupRequestFile)); err != nil || !strings.Contains(string(b), `"kind":"backup"`) {
		t.Fatalf("request file: %s %v", b, err)
	}
	_ = json.Unmarshal(run.Body.Bytes(), &v)
	if v.Pending != "backup" {
		t.Fatalf("pending = %q", v.Pending)
	}
}

// The status the host writes turns into notices when a backup fails or none has run for too long.
func TestBackupNotices(t *testing.T) {
	s, dir := newBackupServer(t)
	cfg := backupConfig{Enabled: true, Hour: 2, Keep: 7}
	b, _ := json.Marshal(cfg)
	_ = s.st.MetaSet(t.Context(), backupConfigKey, string(b))
	status := map[string]any{"version": 1, "configured": true, "last_ok_at": time.Now().Add(-48 * time.Hour).Unix(),
		"last_run": map[string]any{"at": time.Now().Unix(), "ok": false, "error": "pg_dump: connection refused"}}
	raw, _ := json.Marshal(status)
	_ = os.WriteFile(filepath.Join(dir, backupStatusFile), raw, 0o644)
	keys := map[string]string{}
	for _, n := range s.backupNotices(t.Context()) {
		keys[n.key] = n.detail
	}
	if keys["backup-failed"] != "pg_dump: connection refused" || !strings.Contains(keys["backup-overdue"], "48 hours") {
		t.Fatalf("notices = %v", keys)
	}
	cfg.Enabled = false
	b, _ = json.Marshal(cfg)
	_ = s.st.MetaSet(t.Context(), backupConfigKey, string(b))
	if n := s.backupNotices(t.Context()); len(n) != 0 {
		t.Fatalf("backups off: no notices, got %+v", n)
	}
}

// The database copy is a complete, openable database.
func TestBackupDBInto(t *testing.T) {
	s, _ := newBackupServer(t)
	_ = s.st.MetaSet(t.Context(), "probe", "value")
	out := filepath.Join(t.TempDir(), "copy.db")
	if err := s.st.VacuumInto(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	cp, err := store.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if v, ok, _ := cp.MetaGet(t.Context(), "probe"); !ok || v != "value" {
		t.Fatalf("copy lost its data: %q %v", v, ok)
	}
	if err := backupDBInto(t.Context(), s.st, "relative/path.db"); err == nil {
		t.Fatal("a relative path is refused")
	}
}
