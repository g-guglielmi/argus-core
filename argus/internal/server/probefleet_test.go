// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// updateStatus must never propose a downgrade: a probe at or ahead of the GHCR-resolved latest is
// current (the latest cache lags a just-published release), while a genuinely older probe is outdated.
// An explicit pin is a deliberate target, so any mismatch converges to it.
func TestUpdateStatus(t *testing.T) {
	cases := []struct {
		name                     string
		reported, target, latest string
		want                     string
	}{
		{"no version yet", "", "latest", "7.0.30-r9", "unknown"},
		{"latest not resolved", "7.0.30-r9", "latest", "", "tracking"},
		{"equal to latest", "7.0.30-r9", "latest", "7.0.30-r9", "current"},
		{"behind latest", "7.0.30-r8", "latest", "7.0.30-r9", "outdated"},
		// The reported case: fleet already on r10 while the GHCR cache still says r9 - must NOT be
		// flagged outdated (that would offer a downgrade to r9).
		{"ahead of stale latest", "7.0.30-r10", "latest", "7.0.30-r9", "current"},
		{"ahead by patch", "7.0.31-r1", "latest", "7.0.30-r9", "current"},
		// Explicit pins are deliberate: match is current, anything else (older or newer) converges.
		{"pin matched", "7.0.30-r9", "7.0.30-r9", "7.0.30-r10", "current"},
		{"pin older than running", "7.0.30-r10", "7.0.29-r1", "7.0.30-r10", "outdated"},
		{"pin newer than running", "7.0.30-r8", "7.0.30-r9", "7.0.30-r9", "outdated"},
	}
	for _, c := range cases {
		if got := updateStatus(c.reported, c.target, c.latest); got != c.want {
			t.Errorf("%s: updateStatus(%q,%q,%q)=%q, want %q", c.name, c.reported, c.target, c.latest, got, c.want)
		}
	}
}
