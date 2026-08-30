package server

import "testing"

func TestAppUpdateStatus(t *testing.T) {
	cases := []struct {
		name       string
		cur, latest string
		wantStatus string
		wantUpdate bool
	}{
		{"unstamped local build", "", "v0.4.10", "dev", false},
		{"bare sha build", "abc1234", "v0.4.10", "dev", false},
		{"latest not resolved yet", "v0.4.10", "", "unknown", false},
		{"exact newest release", "v0.4.10", "v0.4.10", "current", false},
		{"pinned older release", "v0.4.9", "v0.4.10", "outdated", true},
		{"older by minor", "v0.3.0", "v0.4.10", "outdated", true},
		// A git-describe build one commit past the newest tag is unreleased code, not the latest release.
		{"testing build ahead of last release", "v0.4.10-3-gabc1234", "v0.4.10", "development", false},
		{"testing build one commit ahead", "v0.4.10-1-g9688545", "v0.4.10", "development", false},
		{"dirty worktree on the newest tag", "v0.4.10-dirty", "v0.4.10", "development", false},
		{"dirty build past a tag", "v0.4.10-2-gabc1234-dirty", "v0.4.10", "development", false},
		// A development build is always "development", never "outdated" - even when its numeric base
		// trails the newest release (its code is unreleased and an in-place update can't reach the
		// release tag; the digest-based dev-channel check decides if a newer testing image exists).
		{"dev build numerically behind a release", "v0.4.9-2-gdead", "v0.4.10", "development", false},
		{"testing build of the release commit", "v0.4.15-9-g0517cd4", "v0.4.16", "development", false},
		// A clean base newer than anything published is still ahead of the newest release, not "current".
		{"ahead of published (newer major)", "v1.0.0", "v0.4.10", "development", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotStatus, gotUpdate := appUpdateStatus(c.cur, c.latest)
			if gotStatus != c.wantStatus || gotUpdate != c.wantUpdate {
				t.Errorf("appUpdateStatus(%q, %q) = (%q, %v), want (%q, %v)",
					c.cur, c.latest, gotStatus, gotUpdate, c.wantStatus, c.wantUpdate)
			}
		})
	}
}

// TestRunningImageRef checks which GHCR tag identifies the running build for the :testing digest
// compare - especially a clean release tag, the "aligned" case where :testing == :latest right after a
// release and the running version alone can't distinguish the channels.
func TestRunningImageRef(t *testing.T) {
	cases := []struct{ cur, want string }{
		{"v0.4.18-6-g1baec4b", "sha-1baec4b"},   // dev build -> its commit image
		{"v0.4.15-9-g0517cd4", "sha-0517cd4"},   // dev build
		{"v0.4.18", "v0.4.18"},                  // clean release (aligned case) -> the release tag
		{"0.4.18", "0.4.18"},                    // clean, no leading v
		{"v0.4.18-dirty", ""},                   // dirty, no commit -> unidentifiable
		{"", ""},                                // un-stamped
		{"abc1234", ""},                         // bare sha, no semver base
	}
	for _, c := range cases {
		if got := runningImageRef(c.cur); got != c.want {
			t.Errorf("runningImageRef(%q) = %q, want %q", c.cur, got, c.want)
		}
	}
}

func TestDevUpdateOffered(t *testing.T) {
	cases := []struct {
		status string
		devUpd bool
		want   bool
	}{
		{"development", true, true},          // dev build with a newer testing image
		{"current", true, true},              // clean release on :testing with a newer testing image (the bug case)
		{"development", false, false},        // dev build, no newer testing image
		{"current", false, false},            // aligned clean release, nothing newer
		{"outdated", true, false},            // a release-channel box is never offered the testing image here
		{"unknown", true, false},             // can't compare -> don't offer
	}
	for _, c := range cases {
		if got := devUpdateOffered(c.status, c.devUpd); got != c.want {
			t.Errorf("devUpdateOffered(%q, %v) = %v, want %v", c.status, c.devUpd, got, c.want)
		}
	}
}
