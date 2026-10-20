// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
)

func TestRebootWindowValid(t *testing.T) {
	cases := []struct {
		name string
		rw   rebootWindow
		want bool
	}{
		{"notify ignores time fields", rebootWindow{Mode: "notify"}, true},
		{"notify with junk time still valid", rebootWindow{Mode: "notify", Weekday: 99, Hour: 99}, true},
		{"auto in range", rebootWindow{Mode: "auto", Weekday: 3, Hour: 3, Minute: 30}, true},
		{"auto sunday midnight", rebootWindow{Mode: "auto", Weekday: 0, Hour: 0, Minute: 0}, true},
		{"auto saturday last minute", rebootWindow{Mode: "auto", Weekday: 6, Hour: 23, Minute: 59}, true},
		{"empty mode", rebootWindow{}, false},
		{"unknown mode", rebootWindow{Mode: "sometimes"}, false},
		{"auto weekday too high", rebootWindow{Mode: "auto", Weekday: 7}, false},
		{"auto hour too high", rebootWindow{Mode: "auto", Hour: 24}, false},
		{"auto minute too high", rebootWindow{Mode: "auto", Minute: 60}, false},
		{"auto negative", rebootWindow{Mode: "auto", Weekday: -1}, false},
	}
	for _, c := range cases {
		if got := c.rw.valid(); got != c.want {
			t.Errorf("%s: valid() = %v, want %v", c.name, got, c.want)
		}
	}
	if defaultRebootWindow().Mode != "notify" {
		t.Errorf("default reboot window mode = %q, want notify (core must never reboot unannounced)", defaultRebootWindow().Mode)
	}
}

// The fleet's Zabbix version comes from the probes' image versions ("<zabbix>-rN"): the newest
// x.y.z prefix wins, junk is skipped.
func TestFleetZbxVersion(t *testing.T) {
	cases := []struct {
		name     string
		versions []string
		want     string
	}{
		{"empty", nil, ""},
		{"single", []string{"7.0.30-r14"}, "7.0.30"},
		{"newest wins", []string{"7.0.30-r14", "7.0.31-r2", "7.0.29-r1"}, "7.0.31"},
		{"numeric not lexicographic", []string{"7.0.9-r1", "7.0.31-r2"}, "7.0.31"},
		{"major beats minor", []string{"7.0.31-r2", "7.2.1-r1"}, "7.2.1"},
		{"junk skipped", []string{"", "dev", "7.0-r1", "7.0.31-r2"}, "7.0.31"},
		{"no revision suffix", []string{"7.0.31"}, "7.0.31"},
	}
	for _, c := range cases {
		if got := fleetZbxVersion(c.versions); got != c.want {
			t.Errorf("%s: fleetZbxVersion(%v) = %q, want %q", c.name, c.versions, got, c.want)
		}
	}
}

// The timezone shape check is deliberately light (the host's zoneinfo database is the real
// gatekeeper) but must refuse anything that could misbehave in a JSON string or a shell-side sed.
func TestValidTimezone(t *testing.T) {
	good := []string{"Europe/Rome", "UTC", "America/Argentina/Buenos_Aires", "Etc/GMT+2", "US/East-Indiana"}
	bad := []string{"", "/etc/passwd", "Europe/Rome\"x", "zone with spaces", "Europe/Rome;reboot", "-leading", "+plus", strings.Repeat("a", 65)}
	for _, tz := range good {
		if !validTimezone(tz) {
			t.Errorf("validTimezone(%q) = false, want true", tz)
		}
	}
	for _, tz := range bad {
		if validTimezone(tz) {
			t.Errorf("validTimezone(%q) = true, want false", tz)
		}
	}
}

func TestZbxUpdateRefusal(t *testing.T) {
	ok := coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32"}
	if why := zbxUpdateRefusal(ok, 1000); why != "" {
		t.Fatalf("a pending minor on a current host was refused: %s", why)
	}
	cases := []struct {
		name string
		v    coreOSView
		want string
	}{
		{"reporter not wired", coreOSView{}, "not wired up"},
		{"host scripts predate it", coreOSView{Available: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32"}, "predate Update now"},
		{"up to date", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.32", ZbxCandidate: "7.0.32"}, "up to date"},
		{"no candidate", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.32"}, "up to date"},
		{"already requested", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32", ZbxUpdatePending: 900}, "already requested"},
	}
	for _, c := range cases {
		if why := zbxUpdateRefusal(c.v, 1000); !strings.Contains(why, c.want) {
			t.Errorf("%s: got %q, want it to say %q", c.name, why, c.want)
		}
	}
	// A request the host never picked up (no path unit, VM off) stops blocking after 15 minutes.
	stale := ok
	stale.ZbxUpdatePending = 1000 - 16*60
	if why := zbxUpdateRefusal(stale, 1000); why != "" {
		t.Errorf("a stale request still blocks: %s", why)
	}
}
