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

// TestRevisionsMatch guards the commit-aware suppression that stops a same-commit rebuild (a release
// tag build and a main-push build of the identical commit produce different digests) from surfacing
// as a phantom ":testing" update on a just-released box.
func TestRevisionsMatch(t *testing.T) {
	const sha = "0a5d0420e7e86af60a4fee0ed0f315b445e372f7"
	cases := []struct {
		name                string
		testingRev, running string
		want                bool
	}{
		{"same commit, rebuilt", sha, sha, true},                 // the loop case: different digest, same code
		{"different commits", sha, "deadbeefcafebabe", false},    // a genuinely newer :testing build
		{"testing revision missing", "", sha, false},             // can't establish -> don't suppress
		{"running revision missing", sha, "", false},             // can't establish -> don't suppress
		{"both missing", "", "", false},                          // pre-label images -> fall back to digest
	}
	for _, c := range cases {
		if got := revisionsMatch(c.testingRev, c.running); got != c.want {
			t.Errorf("revisionsMatch(%q, %q) = %v, want %v", c.testingRev, c.running, got, c.want)
		}
	}
}

// TestChannelFor covers the fix where a dev-stamped build is the testing channel regardless of a
// sidecar's reported tag - a build ahead of the newest release can only be :testing.
func TestChannelFor(t *testing.T) {
	cases := []struct {
		name, version, tag string
		hasReport          bool
		want               string
	}{
		{"dev build, sidecar wrongly reports latest", "v0.4.30-1-g572be7b", "latest", true, "testing"},
		{"dev build, no sidecar report", "v0.4.30-1-g572be7b", "", false, "testing"},
		{"dev build, sidecar reports testing", "v0.4.30-1-g572be7b", "testing", true, "testing"},
		{"clean build on testing", "v0.4.30", "testing", true, "testing"},
		{"clean build on latest", "v0.4.30", "latest", true, "latest"},
		{"clean build, no report -> safe default", "v0.4.30", "", false, "latest"},
	}
	for _, c := range cases {
		if got := channelFor(c.version, c.tag, c.hasReport); got != c.want {
			t.Errorf("%s: channelFor(%q,%q,%v)=%q want %q", c.name, c.version, c.tag, c.hasReport, got, c.want)
		}
	}
}
