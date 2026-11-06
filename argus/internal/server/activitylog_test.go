// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/syslogout"
	"argus/internal/zabbix"
)

// Sign-ins land in the activity log with how and from where, failed ones with why, sign-outs too, and
// each goes to the syslog server as an RFC 5424 message.
func TestSignInActivity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}

	// A UDP syslog server.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if err := mgr.Set(ctx, map[string]string{settings.KeySyslogServer: pc.LocalAddr().String(), settings.KeySyslogProto: "udp", settings.KeySyslogFacil: "local4"}); err != nil {
		t.Fatal(err)
	}

	hash, _ := auth.HashPassword("right-password-123")
	if _, err := st.CreateUser(ctx, store.User{Email: "ops@example.com", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	dummy, _ := auth.HashPassword("x")
	s := &Server{st: st, mgr: mgr, loginLimiter: mgr.Limiter(), dummyHash: dummy, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.startSyslog(sctx)

	login := func(email, pw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"email":"`+email+`","password":"`+pw+`"}`))
		r.RemoteAddr = "10.0.0.5:51000"
		w := httptest.NewRecorder()
		s.handleLogin(w, r)
		return w
	}
	if w := login("ops@example.com", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := login("nobody@example.com", "x"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown account: %d", w.Code)
	}
	w := login("ops@example.com", "right-password-123")
	if w.Code != http.StatusOK {
		t.Fatalf("right password: %d %s", w.Code, w.Body)
	}
	out := httptest.NewRequest("POST", "/api/logout", nil)
	out.RemoteAddr = "10.0.0.5:51001"
	for _, c := range w.Result().Cookies() {
		out.AddCookie(c)
	}
	s.handleLogout(httptest.NewRecorder(), out)

	// The entries are written in the background: wait for all four.
	var got []store.Change
	for i := 0; i < 100; i++ {
		got, _ = st.ListChanges(ctx, store.ChangeQuery{Category: "signins"})
		if len(got) >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := map[string]string{
		"Sign-in failed|wrong password|ops@example.com":     "",
		"Sign-in failed|unknown account|nobody@example.com": "",
		"Signed in|password|ops@example.com":                "",
		"Signed out||ops@example.com":                       "",
	}
	for _, c := range got {
		k := c.Action + "|" + c.Detail + "|" + c.Actor
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected entry %q", k)
			continue
		}
		if c.IP != "10.0.0.5" {
			t.Errorf("%q from %q, want 10.0.0.5", k, c.IP)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing entry %q", k)
	}

	// And on the syslog server: four messages, the failures at notice, local4.
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	var msgs []string
	for len(msgs) < 4 {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		msgs = append(msgs, string(buf[:n]))
	}
	if len(msgs) != 4 {
		t.Fatalf("syslog got %d messages: %q", len(msgs), msgs)
	}
	joined := strings.Join(msgs, "\n")
	for _, frag := range []string{
		"<165>1 ", // local4 (20) * 8 + notice (5): a failed sign-in
		"<166>1 ", // local4 * 8 + info (6): signed in, signed out
		` signin [argus@32473 actor="ops@example.com" actor_kind="user" ip="10.0.0.5" category="signins" action="Signed in" detail="password"`,
		"ops@example.com Sign-in failed (wrong password) from 10.0.0.5",
		"nobody@example.com Sign-in failed (unknown account) from 10.0.0.5",
	} {
		if !strings.Contains(joined, frag) {
			t.Errorf("syslog lacks %q in:\n%s", frag, joined)
		}
	}
}

// A change becomes one syslog message with its before and after.
func TestSyslogEntryOfAChange(t *testing.T) {
	e := syslogEntry(store.Change{At: 1792682483, ActorKind: "user", Actor: "ops@example.com", IP: "10.0.0.7", Category: "hosts",
		Action: "Paused a host", Object: "web1", Detail: "until tomorrow", Reason: "patching",
		Diff: []store.ChangeDiff{{Field: "Interval", Old: "1m", New: "5m"}, {Field: "Tag", New: "lab"}}})
	if e.Severity != syslogout.SevInfo || e.MsgID != "change" || e.Message != "ops@example.com Paused a host: web1 (until tomorrow) from 10.0.0.7" {
		t.Fatalf("%+v", e)
	}
	line := syslogout.Format(syslogout.Config{Facility: 16, Hostname: "argus"}, e)
	if !strings.Contains(line, `changes="Interval: 1m -> 5m; Tag: - -> lab"`) || !strings.Contains(line, `reason="patching"`) {
		t.Fatalf("%s", line)
	}
}
