// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

func TestUpdaterStatus(t *testing.T) {
	cases := []struct{ reported, latest, want string }{
		{"", "0.2.3", "unknown"},        // sidecar version not reported yet
		{"0.2.3", "", "unknown"},        // GHCR not resolved yet
		{"0.2.3", "0.2.3", "current"},   // equal
		{"v0.2.3", "0.2.3", "current"},  // leading "v" ignored on the reported side
		{"0.2.3", "v0.2.4", "outdated"}, // and on the latest side
		{"0.2.2", "0.2.3", "outdated"},  // older
		// A sidecar tracking :latest reports a git-describe build whose BASE == the newest release.
		// Its base is not older, so it's "current" - never a phantom "outdated" the Update button
		// can't clear (the regression this guards; the whole-string compare returned "outdated").
		{"v0.2.3-3-gbe884d6", "0.2.3", "current"},
		{"v0.2.3-3-gbe884d6-dirty", "0.2.3", "current"},
		{"v0.2.4-1-gabc", "0.2.3", "current"},   // describe build of a newer base
		{"v0.3.0", "0.2.3", "current"},          // a clean build ahead of the newest release
		{"v0.2.2-9-gdead", "0.2.3", "outdated"}, // describe build of an OLDER base is still behind
	}
	for _, c := range cases {
		if got := updaterStatus(c.reported, c.latest); got != c.want {
			t.Errorf("updaterStatus(%q,%q) = %q, want %q", c.reported, c.latest, got, c.want)
		}
	}
}
