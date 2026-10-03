// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
)

// A sensor's note goes out with its alert, its reminders and its RESOLVED, in every channel's layout;
// an acknowledged notice keeps to the acknowledger's own note.
func TestSensorNoteInAlerts(t *testing.T) {
	const want = "Note: ISP ticket 4471 open (Alice Rossi)"
	for _, kind := range []string{"problem", "reminder", "recovery"} {
		e := sampleProblem()
		e.Kind, e.Note, e.NoteBy = kind, "ISP ticket 4471 open", "Alice Rossi"
		if kind == "reminder" {
			e.Reminder, e.SinceSecs = 1, 600
		}
		if kind == "recovery" {
			e.State, e.SinceSecs = "ok", 600
		}
		if !strings.Contains(strings.Join(e.bodyLines(), "\n"), want) {
			t.Errorf("%s: email text has no note:\n%s", kind, strings.Join(e.bodyLines(), "\n"))
		}
		if !strings.Contains(strings.Join(e.cardLines(), "\n"), want) {
			t.Errorf("%s: card has no note:\n%s", kind, strings.Join(e.cardLines(), "\n"))
		}
		if text, _ := telegramMessage(e); !strings.Contains(text, "<i>"+htmlEscape(want)+"</i>") {
			t.Errorf("%s: telegram has no note:\n%s", kind, text)
		}
		if !strings.Contains(htmlBody(e), htmlEscape(want)) {
			t.Errorf("%s: email html has no note", kind)
		}
		if p := webhookPayload(e); p.Note != "ISP ticket 4471 open" || p.NoteBy != "Alice Rossi" {
			t.Errorf("%s: webhook note = %q by %q", kind, p.Note, p.NoteBy)
		}
	}

	e := sampleAck()
	e.Note, e.NoteBy = "ISP ticket 4471 open", "Alice Rossi"
	if strings.Contains(strings.Join(e.cardLines(), "\n"), "ISP ticket") || webhookPayload(e).Note != "" {
		t.Errorf("an acknowledged notice carries the sensor note:\n%s", strings.Join(e.cardLines(), "\n"))
	}

	e = sampleProblem()
	e.Note = "on site"
	if got := e.noteLine(); got != "Note: on site" {
		t.Errorf("a note without an author = %q", got)
	}
}

// A device whose ping went down names the hosts behind it in every channel; its RESOLVED says their
// own alerts go out now; an acknowledgement doesn't repeat them.
func TestBehindInAlerts(t *testing.T) {
	for kind, want := range map[string]string{
		"problem":  "Behind it: ap-lobby, sw-floor2 (their alerts wait while it is down)",
		"recovery": "Behind it: ap-lobby, sw-floor2 (their own alerts go out now if they are still in trouble)",
	} {
		e := sampleProblem()
		e.Kind, e.Behind = kind, "ap-lobby, sw-floor2"
		if kind == "recovery" {
			e.State, e.SinceSecs = "ok", 600
		}
		if !strings.Contains(strings.Join(e.bodyLines(), "\n"), want) || !strings.Contains(strings.Join(e.cardLines(), "\n"), want) {
			t.Errorf("%s: text lacks %q", kind, want)
		}
		if text, _ := telegramMessage(e); !strings.Contains(text, htmlEscape(want)) {
			t.Errorf("%s: telegram lacks it:\n%s", kind, text)
		}
		if !strings.Contains(htmlBody(e), htmlEscape(want)) {
			t.Errorf("%s: email html lacks it", kind)
		}
		if p := webhookPayload(e); p.Behind != "ap-lobby, sw-floor2" {
			t.Errorf("%s: webhook behind = %q", kind, p.Behind)
		}
	}
	e := sampleProblem()
	e.Kind, e.Behind = "ack", "ap-lobby"
	if strings.Contains(strings.Join(e.bodyLines(), "\n"), "Behind it") {
		t.Error("an acknowledgement named the hosts behind")
	}
}
