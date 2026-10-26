// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
	"time"

	"argus/internal/mailread"
	"argus/internal/store"
)

// A mail is a job's when its sender and subject contain what the rule says; a failure word (the
// subject first, then the text) makes a failed run, else a success word a good one, else a failed run
// that says neither was there. Words are found where a word starts, in any case.
func TestMatchMail(t *testing.T) {
	rule := store.PushMailRule{Mailbox: 1, From: "veeam@", Subject: "backup job nas", OK: "Success, Completed", Fail: "fail, error"}
	at := time.Now()
	cases := []struct {
		from, subject, text string
		hit, ok             bool
		msg                 string
	}{
		{"Veeam <VEEAM@example.com>", "[Success] Backup job NAS", "12 VMs, 0 errors", true, true, "[Success] Backup job NAS"},
		{"veeam@example.com", "[Failed] Backup job NAS", "", true, false, "[Failed] Backup job NAS"},
		{"veeam@example.com", "Backup job NAS", "The job completed.", true, true, "Backup job NAS"},
		{"veeam@example.com", "Backup job NAS", "Errors: disk full", true, false, "Backup job NAS"},
		{"veeam@example.com", "Backup job NAS ran", "see attachment", true, false, "Neither a success nor a failure word: Backup job NAS ran"},
		{"nas@example.com", "[Success] Backup job NAS", "", false, false, ""},
		{"veeam@example.com", "[Success] Backup job Offsite", "", false, false, ""},
		{"veeam@example.com", "Backup job NAS: Unsuccessful", "", true, false, "Neither a success nor a failure word: Backup job NAS: Unsuccessful"},
	}
	for i, c := range cases {
		hit, ok, msg := matchMail(rule, mailread.Mail{From: c.from, Subject: c.subject, Text: c.text, At: at})
		if hit != c.hit || ok != c.ok || msg != c.msg {
			t.Errorf("case %d (%s): got %v %v %q, want %v %v %q", i, c.subject, hit, ok, msg, c.hit, c.ok, c.msg)
		}
	}
	if hit, _, _ := matchMail(store.PushMailRule{Mailbox: 1, OK: "ok"}, mailread.Mail{Subject: "ok"}); hit {
		t.Error("a rule without a sender or a subject matches nothing")
	}
}

// A mailbox is checked before it is saved: its kind's fields, a secret on a new one, and the defaults
// (port, folder) filled in.
func TestMailboxValidate(t *testing.T) {
	ok := mailboxRequest{Name: "NAS reports", Kind: "imap", Host: "imap.example.com", Security: "starttls", Username: "nas", Secret: "pw"}
	if msg := ok.validate(false); msg != "" || ok.Port != 143 || ok.Folder != "INBOX" {
		t.Fatalf("imap: %q %+v", msg, ok)
	}
	g := mailboxRequest{Name: "Backups", Kind: "graph", Tenant: "example.onmicrosoft.com", ClientID: "11111111-1111-1111-1111-111111111111", Address: "backups@example.com"}
	if msg := g.validate(true); msg != "" || g.Folder != "Inbox" {
		t.Fatalf("graph, secret kept: %q %+v", msg, g)
	}
	for name, bad := range map[string]mailboxRequest{
		"no secret":    {Name: "x", Kind: "imap", Host: "imap.example.com", Security: "tls", Username: "u"},
		"bad host":     {Name: "x", Kind: "imap", Host: "imap.example.com/x", Security: "tls", Username: "u", Secret: "p"},
		"bad security": {Name: "x", Kind: "imap", Host: "imap.example.com", Security: "plain", Username: "u", Secret: "p"},
		"bad client":   {Name: "x", Kind: "graph", Tenant: "t.example.com", ClientID: "app", Address: "a@example.com", Secret: "s"},
		"bad address":  {Name: "x", Kind: "graph", Tenant: "t.example.com", ClientID: "11111111-1111-1111-1111-111111111111", Address: "a@example.com/x", Secret: "s"},
		"no name":      {Kind: "imap", Host: "imap.example.com", Security: "tls", Username: "u", Secret: "p"},
	} {
		if msg := bad.validate(false); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A push sensor that reads a mailbox that can't be read says so once it is late, instead of its last
// run; one that isn't late yet, and one that doesn't read email, keep theirs.
func TestPushPollMailboxTrouble(t *testing.T) {
	now := int64(1_000_000)
	trouble := map[int64]string{7: "its mailbox NAS reports can't be read since Oct 26 08:12: the server refused the login"}
	got := pushPoll([]store.PushSensor{
		{ID: 1, Name: "late", LastAt: now - 7200, LastOK: true, LastMsg: "[Success] job", LateSecs: 3600, MissedSecs: 7200, Mail: store.PushMailRule{Mailbox: 7}},
		{ID: 2, Name: "fresh", LastAt: now - 60, LastOK: true, LastMsg: "[Success] job", LateSecs: 3600, MissedSecs: 7200, Mail: store.PushMailRule{Mailbox: 7}},
		{ID: 3, Name: "url", LastAt: now - 7200, LastOK: true, LastMsg: "done", LateSecs: 3600, MissedSecs: 7200},
	}, now, trouble)
	if !strings.HasPrefix(got[0].Message, "No run read: its mailbox NAS reports can't be read") || got[1].Message != "[Success] job" || got[2].Message != "done" {
		t.Fatalf("messages: %q / %q / %q", got[0].Message, got[1].Message, got[2].Message)
	}
}
