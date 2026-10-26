// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
	"time"
)

// The feed is an iCalendar a calendar app takes: one event per run with UTC times and a UID stable per
// run, text escaped, lines folded at 75 octets without splitting a character.
func TestBuildICS(t *testing.T) {
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	runs := []calOccurrence{{ID: 3, Name: "Weekly patching; site1, site2", Start: start.Unix(), End: start.Add(2 * time.Hour).Unix()}}
	ics := buildICS(runs, map[int64]string{3: strings.Repeat("très-long-host-name, ", 8)}, start)
	for _, want := range []string{"BEGIN:VCALENDAR\r\n", "UID:argus-maintenance-3-" + "1792800000" + "@argus\r\n", "DTSTART:20261024T000000Z\r\n", "DTEND:20261024T020000Z\r\n",
		`SUMMARY:Maintenance: Weekly patching\; site1\, site2` + "\r\n", "END:VCALENDAR\r\n"} {
		if !strings.Contains(ics, want) {
			t.Errorf("missing %q in\n%s", want, ics)
		}
	}
	for _, l := range strings.Split(ics, "\r\n") {
		if len(l) > 75 {
			t.Errorf("a line longer than 75 octets: %q", l)
		}
		if !utf8Valid(l) {
			t.Errorf("a fold split a character: %q", l)
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }
