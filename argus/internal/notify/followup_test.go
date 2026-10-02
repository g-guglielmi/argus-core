// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
)

func sampleReminder() Event {
	e := sampleProblem()
	e.Kind, e.Reminder, e.SinceSecs = "reminder", 2, 3900
	return e
}

func sampleAck() Event {
	e := sampleProblem()
	e.Kind, e.AckURL, e.AckBy, e.AckNote = "ack", "", "Alice Rossi", "On site, swapping the PSU"
	return e
}

// A reminder keeps the severity and the Acknowledge action, and says how long it has been open.
func TestReminderMessages(t *testing.T) {
	e := sampleReminder()
	if got := e.subject(); got != "[HIGH REMINDER] sw-site2 - Unavailable by ICMP ping" {
		t.Fatalf("subject = %q", got)
	}
	body := strings.Join(e.bodyLines(), "\n")
	for _, want := range []string{"Still open after 1h 5m (reminder 2).", "Severity: High", "Value: 100 %"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	text, kb := telegramMessage(e)
	if !strings.Contains(text, "Still open after 1h 5m (reminder 2).") || len(kb) != 1 || len(kb[0]) != 2 {
		t.Fatalf("telegram: %s %v", text, kb)
	}
	if h := htmlBody(e); !strings.Contains(h, ">Acknowledge<") || !strings.Contains(h, "Still open after 1h 5m") {
		t.Errorf("reminder html: %s", h)
	}
}

// The acknowledged notice names who took it and their note, in its own colour, with no Acknowledge action.
func TestAckMessages(t *testing.T) {
	e := sampleAck()
	if got := e.subject(); got != "[ACKNOWLEDGED] sw-site2 - Unavailable by ICMP ping" {
		t.Fatalf("subject = %q", got)
	}
	if e.emoji() != "🔵" || e.color() != colorAck || htmlColor(e) != "#3b82f6" {
		t.Fatalf("ack look: %s %x %s", e.emoji(), e.color(), htmlColor(e))
	}
	body := strings.Join(e.bodyLines(), "\n")
	if !strings.Contains(body, "Acknowledged by Alice Rossi.") || !strings.Contains(body, "Note: On site, swapping the PSU") ||
		strings.Contains(body, "Severity:") || strings.Contains(body, "Value:") || !strings.Contains(body, "Time (acknowledged)") {
		t.Fatalf("body:\n%s", body)
	}
	text, kb := telegramMessage(e)
	if !strings.Contains(text, "Acknowledged by Alice Rossi.") || !strings.Contains(text, "<i>On site, swapping the PSU</i>") {
		t.Fatalf("telegram text: %s", text)
	}
	if len(kb) != 1 || len(kb[0]) != 1 || kb[0][0]["text"] != "Open in Argus" {
		t.Fatalf("telegram keyboard = %v", kb)
	}
	if h := htmlBody(e); strings.Contains(h, ">Acknowledge<") || !strings.Contains(h, "Acknowledged at") {
		t.Errorf("ack html: %s", h)
	}
	e.AckBy = ""
	if e.ackLine() != "Acknowledged." {
		t.Errorf("anonymous ack line = %q", e.ackLine())
	}
}

// A system notice: its own tag and colour, no host when it's about Argus itself, its explanation in the
// body, and no Acknowledge action.
func TestInfoMessages(t *testing.T) {
	e := Event{Kind: "info", Name: "Argus 0.6.0 is available", Detail: "You run 0.5.9.\nUpdate it on the Updates page.",
		When: sampleProblem().When, OpenURL: "https://monitoring.example.com/?view=updates"}
	if got := e.subject(); got != "[INFO] Argus 0.6.0 is available" {
		t.Fatalf("subject = %q", got)
	}
	if e.color() != colorInfo || htmlColor(e) != "#2ea8c9" {
		t.Fatalf("info colour: %x %s", e.color(), htmlColor(e))
	}
	body := strings.Join(e.bodyLines(), "\n")
	if !strings.Contains(body, "You run 0.5.9.") || !strings.Contains(body, "Update it on the Updates page") || strings.Contains(body, "Host:") || strings.Contains(body, "Severity") {
		t.Fatalf("body:\n%s", body)
	}
	text, kb := telegramMessage(e)
	if !strings.HasPrefix(text, "ℹ️ <b>[INFO] Argus 0.6.0 is available</b>\nYou run 0.5.9.") {
		t.Fatalf("telegram: %s", text)
	}
	if len(kb) != 1 || len(kb[0]) != 1 {
		t.Fatalf("keyboard: %v", kb)
	}
	h := htmlBody(e)
	if strings.Contains(h, ">Acknowledge<") || strings.Contains(h, ">Host<") || !strings.Contains(h, "You run 0.5.9.") {
		t.Fatalf("html: %s", h)
	}
	p := Event{Kind: "info", Name: "Probe self-update failed", Host: "Probe site1", Site: "site1", When: e.When}
	if got := p.subject(); got != "[INFO] Probe site1 - Probe self-update failed" {
		t.Fatalf("host subject = %q", got)
	}
}
