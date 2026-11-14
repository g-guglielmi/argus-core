// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
)

// The line a push notification shows says what went down or came back, on which host, at which
// site; Discord's copy of it can't carry a masked link.
func TestPushLine(t *testing.T) {
	down := Event{Kind: "problem", State: "error", Severity: 4, Name: "Outlook does not answer from this site", Host: "Probe office", Site: "site1"}
	if got := down.pushLine(); got != "🔴 Outlook does not answer from this site · Probe office · site1" {
		t.Fatalf("problem: %q", got)
	}
	up := Event{Kind: "recovery", State: "ok", Name: "Outlook answers again from this site", Host: "Probe office", Site: "site1"}
	if got := up.pushLine(); got != "🟢 Outlook answers again from this site · Probe office · site1" {
		t.Fatalf("recovery: %q", got)
	}
	rem := down
	rem.Kind = "reminder"
	if got := rem.pushLine(); got != "🔴 Still open: Outlook does not answer from this site · Probe office · site1" {
		t.Fatalf("reminder: %q", got)
	}
	ack := down
	ack.Kind = "ack"
	if got := ack.pushLine(); !strings.HasPrefix(got, "🔵 Acknowledged: Outlook does not answer") {
		t.Fatalf("ack: %q", got)
	}
	notice := Event{Kind: "info", Name: "Argus was updated"}
	if got := notice.pushLine(); got != "ℹ️ Argus was updated" {
		t.Fatalf("a notice about Argus itself: %q", got)
	}
	evil := Event{Kind: "problem", State: "error", Name: "Service not running: [click](https://example.com) `x`", Host: "pc1"}
	if got := discordContent(evil); strings.ContainsAny(got, "[]`") || !strings.Contains(got, "(click)(https://example.com)") {
		t.Fatalf("a masked link got through: %q", got)
	}
	long := Event{Kind: "problem", State: "error", Name: strings.Repeat("x", 500), Host: "pc1"}
	if got := []rune(discordContent(long)); len(got) != discordContentMax {
		t.Fatalf("length: %d", len(got))
	}
}
