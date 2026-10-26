// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
	"time"
)

// What to do goes with an alert and its reminders, line by line, never with a RESOLVED or an
// acknowledgement.
func TestRunbookLines(t *testing.T) {
	rb := "1. Check the PoE port.\n\n2. Power-cycle it.  "
	for _, kind := range []string{"problem", "reminder", "recovery", "ack"} {
		e := Event{Kind: kind, Severity: 4, State: "error", Host: "ap-lobby", Name: "Unavailable by ICMP ping", When: time.Unix(0, 0), Runbook: rb}
		body := strings.Join(e.bodyLines(), "\n")
		want := kind == "problem" || kind == "reminder"
		if strings.Contains(body, "What to do:\n1. Check the PoE port.\n2. Power-cycle it.\n") != want {
			t.Errorf("%s: runbook shown = %v, want %v:\n%s", kind, !want, want, body)
		}
		if (webhookPayload(e).Runbook != "") != want {
			t.Errorf("%s webhook: %q", kind, webhookPayload(e).Runbook)
		}
	}
}
