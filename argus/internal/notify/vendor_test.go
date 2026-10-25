// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package notify

import (
	"strings"
	"testing"
	"time"
)

// A cloud service's vendor line rides on the alert, its reminders and its RESOLVED, in every layout,
// and never on an acknowledgement.
func TestVendorLineRides(t *testing.T) {
	const vl = "GitHub's status page: no incident open"
	for _, kind := range []string{"problem", "reminder", "recovery", "ack"} {
		e := Event{Kind: kind, Severity: 4, State: "error", Host: "Probe site1", Name: "GitHub doesn't answer", When: time.Unix(0, 0), Vendor: vl}
		want := kind != "ack"
		for name, text := range map[string]string{
			"body":    strings.Join(e.bodyLines(), "\n"),
			"card":    strings.Join(e.cardLines(), "\n"),
			"webhook": webhookPayload(e).Vendor,
		} {
			if strings.Contains(text, vl) != want {
				t.Errorf("%s %s: vendor line shown = %v, want %v", kind, name, !want, want)
			}
		}
	}
}
