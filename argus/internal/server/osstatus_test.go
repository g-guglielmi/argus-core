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
		{"auto every day", rebootWindow{Mode: "auto", Weekday: -1, Hour: 3}, true},
		{"auto below every day", rebootWindow{Mode: "auto", Weekday: -2}, false},
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

func TestHostRequestRefusal(t *testing.T) {
	ok := coreOSView{Available: true, ZbxUpdateNow: true, RebootNow: true, PatchNow: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32", RebootRequired: true, SecUpdates: 3}
	for _, kind := range []string{"zbx-update", "reboot", "patch"} {
		if why := hostRequestRefusal(kind, ok, 1000); why != "" {
			t.Fatalf("%s refused on a host that can: %s", kind, why)
		}
	}
	cases := []struct {
		name, kind string
		v          coreOSView
		want       string
	}{
		{"reporter not wired", "zbx-update", coreOSView{}, "not wired up"},
		{"host scripts predate it", "zbx-update", coreOSView{Available: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32"}, "predate Update now"},
		{"up to date", "zbx-update", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.32", ZbxCandidate: "7.0.32"}, "up to date"},
		{"no candidate", "zbx-update", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.32"}, "up to date"},
		{"already requested", "zbx-update", coreOSView{Available: true, ZbxUpdateNow: true, ZbxServer: "7.0.31", ZbxCandidate: "7.0.32", ZbxUpdatePending: 900}, "already requested"},
		{"reboot: scripts predate it", "reboot", coreOSView{Available: true, RebootRequired: true}, "predate Reboot now"},
		{"reboot: not needed", "reboot", coreOSView{Available: true, RebootNow: true}, "does not need a reboot"},
		{"reboot: already requested", "reboot", coreOSView{Available: true, RebootNow: true, RebootRequired: true, RebootPending: 950}, "already requested"},
		{"patch: scripts predate it", "patch", coreOSView{Available: true, SecUpdates: 2}, "predate Patch now"},
		{"patch: nothing pending", "patch", coreOSView{Available: true, PatchNow: true, SecUpdates: 0}, "no security updates"},
		{"patch: count unknown", "patch", coreOSView{Available: true, PatchNow: true, SecUpdates: -1}, "no security updates"},
		{"patch: already requested", "patch", coreOSView{Available: true, PatchNow: true, SecUpdates: 2, PatchPending: 990}, "already requested"},
		{"unknown kind", "wipe", ok, "unknown request"},
		{"tsdb: scripts predate it", "tsdb-update", coreOSView{Available: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5"}, "predate the TimescaleDB update"},
		{"tsdb: not installed", "tsdb-update", coreOSView{Available: true, TsdbUpdateNow: true}, "no TimescaleDB package"},
		{"tsdb: up to date", "tsdb-update", coreOSView{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.5", TsdbCandidate: "2.28.5", TsdbExt: "2.28.5"}, "up to date"},
		{"tsdb: already requested", "tsdb-update", coreOSView{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5", TsdbUpdatePending: 900}, "already requested"},
		{"tsdb: a Zabbix update is in hand", "tsdb-update", coreOSView{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5", ZbxUpdatePending: 900}, "already requested"},
		{"tsdb: a line move is in hand", "tsdb-update", coreOSView{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5", TsdbLineUpdatePending: 900}, "already requested"},
		{"line: scripts predate it", "tsdb-line", coreOSView{Available: true, Tsdb: "2.28.5", TsdbLineCandidate: "2.30.3", BackupOKAt: 990}, "predate the move to another TimescaleDB line"},
		{"line: not installed", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, TsdbLineCandidate: "2.30.3", BackupOKAt: 990}, "no TimescaleDB package"},
		{"line: Zabbix supports nothing higher", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.5", BackupOKAt: 990}, "supports no TimescaleDB line above"},
		{"line: already there", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.30.3", TsdbLineCandidate: "2.30.3", BackupOKAt: 990}, "supports no TimescaleDB line above"},
		{"line: no backup at all", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.5", TsdbLineCandidate: "2.30.3"}, "take a backup of this core first"},
		{"line: the backup is too old", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.5", TsdbLineCandidate: "2.30.3", BackupOKAt: 1000 - 86401}, "take a backup of this core first"},
		{"line: already requested", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.5", TsdbLineCandidate: "2.30.3", BackupOKAt: 990, TsdbLineUpdatePending: 900}, "already requested"},
		{"line: a patch update is in hand", "tsdb-line", coreOSView{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.3", TsdbLineCandidate: "2.30.3", BackupOKAt: 990, TsdbUpdatePending: 900}, "already requested"},
	}
	for _, v := range []coreOSView{
		{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5", TsdbExt: "2.28.3"},
		{Available: true, TsdbUpdateNow: true, Tsdb: "2.28.5", TsdbCandidate: "2.28.5", TsdbExt: "2.28.3"}, // the database is behind its package
	} {
		if why := hostRequestRefusal("tsdb-update", v, 1000); why != "" {
			t.Errorf("tsdb-update refused with %+v: %s", v, why)
		}
	}
	for _, v := range []coreOSView{
		{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.5", TsdbLineCandidate: "2.30.3", BackupOKAt: 1000 - 86400},
		{Available: true, TsdbLineUpdateNow: true, Tsdb: "2.28.3", TsdbCandidate: "2.28.5", TsdbLineCandidate: "2.30.3", BackupOKAt: 990}, // straight from an older patch release
	} {
		if why := hostRequestRefusal("tsdb-line", v, 1000); why != "" {
			t.Errorf("tsdb-line refused with %+v: %s", v, why)
		}
	}
	for in, want := range map[string]string{"2.28.5": "2.28.5", "": "", "2.28.5~debian13": "", "2.28.5\"; rm": "", "1.2.3.4.5.6.7.8.9.10.11.12.13": ""} {
		if got := versionOnly(in); got != want {
			t.Errorf("versionOnly(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range cases {
		if why := hostRequestRefusal(c.kind, c.v, 1000); !strings.Contains(why, c.want) {
			t.Errorf("%s: got %q, want it to say %q", c.name, why, c.want)
		}
	}
	// A request the host never picked up (no path unit, VM off) stops blocking after 15 minutes.
	stale := ok
	stale.ZbxUpdatePending, stale.RebootPending, stale.PatchPending = 1000-16*60, 1000-16*60, 1000-16*60
	for _, kind := range []string{"zbx-update", "reboot", "patch"} {
		if why := hostRequestRefusal(kind, stale, 1000); why != "" {
			t.Errorf("%s: a stale request still blocks: %s", kind, why)
		}
	}
}
