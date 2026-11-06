// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/syslogout"
)

// The activity log (DESIGN section 7c): the change log's entries (who changed what) plus the sign-ins,
// sign-outs and failed sign-ins, each with the address it came from. Every entry is written through
// addChanges, which also hands it to the syslog forwarder when a syslog server is set (Settings,
// Syslog), so the two never differ.

// startSyslog makes the forwarder, reading its target from the settings on every entry.
func (s *Server) startSyslog(ctx context.Context) {
	s.syslog = syslogout.New(func() syslogout.Config {
		t := s.mgr.Syslog()
		return syslogout.Config{Server: t.Server, Protocol: t.Protocol, Facility: t.Facility, Hostname: s.syslogHostname()}
	})
	s.syslog.Start(ctx)
}

// syslogHostname is the HOSTNAME field: the host of the Public URL, else the machine's name.
func (s *Server) syslogHostname() string {
	if u, err := url.Parse(s.mgr.PublicURL()); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	h, _ := os.Hostname()
	return h
}

// addChanges writes activity entries and sends them to syslog.
func (s *Server) addChanges(ctx context.Context, entries []store.Change) error {
	if err := s.st.AddChanges(ctx, entries); err != nil {
		return err
	}
	for _, c := range entries {
		s.syslog.Send(syslogEntry(c))
	}
	return nil
}

// syslogEntry is a change as one syslog message: the fields for a SIEM to filter on, and a line a
// person reads ("admin@example.com Paused a host: web1 (until tomorrow)").
func syslogEntry(c store.Change) syslogout.Entry {
	sev, msgID := syslogout.SevInfo, "change"
	if c.Category == "signins" {
		msgID = "signin"
		if strings.HasPrefix(c.Action, "Sign-in failed") {
			sev = syslogout.SevNotice
		}
	}
	at := time.Unix(c.At, 0)
	if c.At == 0 {
		at = time.Now()
	}
	line := strings.TrimSpace(c.Actor + " " + c.Action)
	if c.Object != "" {
		line += ": " + c.Object
	}
	if c.Detail != "" {
		line += " (" + c.Detail + ")"
	}
	if c.IP != "" {
		line += " from " + c.IP
	}
	diff := make([]string, 0, len(c.Diff))
	for _, d := range c.Diff {
		diff = append(diff, d.Field+": "+orDash(d.Old)+" -> "+orDash(d.New))
	}
	return syslogout.Entry{At: at, Severity: sev, MsgID: msgID, Message: line, Fields: [][2]string{
		{"actor", c.Actor}, {"actor_kind", c.ActorKind}, {"ip", c.IP}, {"category", c.Category}, {"action", c.Action},
		{"object", c.Object}, {"detail", c.Detail}, {"changes", strings.Join(diff, "; ")}, {"reason", c.Reason}, {"request_id", c.RequestID},
	}}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// noteSignIn writes a sign-in, a sign-out or a failed sign-in to the activity log. u is the account
// when it is known; who is the address typed when it isn't.
func (s *Server) noteSignIn(r *http.Request, u *store.User, who, action, detail string) {
	c := store.Change{At: time.Now().Unix(), ActorKind: "user", Category: "signins", Action: action, Detail: detail, IP: s.clientIP(r), RequestID: newRequestID()}
	if u != nil {
		c.ActorID, c.Actor = u.ID, u.Email
	} else {
		c.Actor = clip(printable(strings.TrimSpace(who)), 120)
		if c.Actor == "" {
			c.Actor = "(no address typed)"
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.addChanges(ctx, []store.Change{c}); err != nil {
			s.logger.Warn("activity log: could not write a sign-in", "err", err)
		}
	}()
}

// GET /api/settings/syslog - how sending has gone (admin).
func (s *Server) handleSyslogStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.syslog.Status())
}

// POST /api/settings/syslog/test - sends one message now and says whether it went (admin).
func (s *Server) handleSyslogTest(w http.ResponseWriter, r *http.Request) {
	who := "an admin"
	if u, _ := auth.UserFrom(r.Context()); u != nil {
		who = u.Email
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := s.syslog.Test(ctx, syslogout.Entry{Severity: syslogout.SevInfo, MsgID: "test",
		Message: "Argus test message, sent by " + who + " from Settings",
		Fields:  [][2]string{{"actor", who}, {"ip", s.clientIP(r)}, {"category", "test"}}})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not send to the syslog server: " + err.Error()})
		return
	}
	t := s.mgr.Syslog()
	note := "sent"
	if t.Protocol == "udp" {
		note = "sent over UDP, which has no answer: check that it arrived on the server"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": note})
}
