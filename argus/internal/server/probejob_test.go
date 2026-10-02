// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// A probe update reads queued, then updating until the probe reports the new version, and an update
// that didn't take stays shown for a day.
func TestProbeJobOf(t *testing.T) {
	const now = int64(1_800_000_000)
	ago := func(s int64) string { return itoa64(now - s) }
	cases := []struct {
		name                    string
		queued, handed, failed  string
		version, newest, wantSt string
	}{
		{"queued", "latest", "", "", "7.0.31-r21", "7.0.31-r22", "queued"},
		{"handed out, still on the old version", "", "latest|" + ago(60), "", "7.0.31-r21", "7.0.31-r22", "updating"},
		{"a rolling tag is done on the newest version", "", "latest|" + ago(60), "", "7.0.31-r22", "7.0.31-r22", ""},
		{"a pin is done on that version", "", "7.0.31-r22|" + ago(60), "", "7.0.31-r22", "", ""},
		{"didn't take", "", "", "latest|" + ago(3600), "7.0.31-r21", "7.0.31-r22", "failed"},
		{"a day-old failure is gone", "", "", "latest|" + ago(90000), "7.0.31-r21", "7.0.31-r22", ""},
		{"nothing", "", "", "", "7.0.31-r22", "7.0.31-r22", ""},
	}
	for _, c := range cases {
		got := probeJobOf(c.queued, c.handed, c.failed, c.version, c.newest, now)
		st := ""
		if got != nil {
			st = got.State
		}
		if st != c.wantSt {
			t.Errorf("%s: state %q, want %q", c.name, st, c.wantSt)
		}
	}
	if j := probeJobOf("", "latest|"+ago(60), "", "7.0.31-r21", "7.0.31-r22", now); j.At != now-60 || j.Tag != "latest" {
		t.Errorf("updating carries its tag and hand-out time: %+v", j)
	}
}
