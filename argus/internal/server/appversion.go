package server

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/buildinfo"
)

// appImageRepo is the public GHCR repository for the Argus app image. Kept in step with build.yml's
// image name; a fork changes both.
const appImageRepo = "ghcr.io/g-guglielmi/argus"

// appGitHubRepo is the "owner/repo" the app image is built from, used to fetch a release's notes (the
// CHANGELOG section published as the GitHub Release body). Kept in step with appImageRepo.
const appGitHubRepo = "g-guglielmi/argus"

// appLatestRefresh is how often we re-poll GHCR for the newest published release.
const appLatestRefresh = 3 * time.Hour

// appLatestCache holds the newest published app release (vX.Y.Z) plus its release notes, resolved from
// public GHCR (tags) + the GitHub Releases API and refreshed periodically, so the UI can flag "update
// available" and show what changed instead of guessing.
type appLatestCache struct {
	mu        sync.RWMutex
	version   string
	notes     string // release body (CHANGELOG section) for `version`, "" if not fetched
	devUpdate bool   // a development (:testing) build is running and a newer :testing image exists
}

func (c *appLatestCache) get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

func (c *appLatestCache) getDevUpdate() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.devUpdate
}

func (c *appLatestCache) setDevUpdate(v bool) {
	c.mu.Lock()
	c.devUpdate = v
	c.mu.Unlock()
}

// getNotes returns the cached release version and its notes together, so the two are always consistent.
func (c *appLatestCache) getNotes() (version, notes string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version, c.notes
}

func (c *appLatestCache) set(v string) {
	c.mu.Lock()
	c.version = v
	c.mu.Unlock()
}

func (c *appLatestCache) setNotes(v, notes string) {
	c.mu.Lock()
	c.version, c.notes = v, notes
	c.mu.Unlock()
}

// startAppLatestRefresh kicks off a background poller: an immediate resolve, then every few hours.
// Non-blocking (startup never waits on GHCR); failures leave the cache empty and the UI shows the
// running version without an up-to-date verdict.
func (s *Server) startAppLatestRefresh(ctx context.Context) {
	go func() {
		refresh := func() {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			v, err := resolveLatestAppVersion(c)
			if err != nil {
				s.logger.Warn("app latest: GHCR resolve failed", "err", err)
				return
			}
			if v == "" {
				return
			}
			// Best-effort: fetch the release notes so the UI can show what changed. A failure here
			// (rate limit, no matching release) just leaves notes empty - the version verdict stands.
			notes, nerr := resolveReleaseNotes(c, v)
			if nerr != nil {
				s.logger.Warn("app latest: release notes fetch failed", "version", v, "err", nerr)
			}
			s.appLatest.setNotes(v, notes)
			s.logger.Info("app latest resolved from GHCR", "version", v, "notes", notes != "")

			// Development-channel check: a :testing build reads as "development" against releases
			// (it's ahead of the newest v*), so the release comparison alone never flags an update
			// for it. Compare the running commit's image digest to the current :testing digest so a
			// box tracking :testing still learns when a newer testing build has been published.
			dev, derr := resolveDevChannelUpdate(c, buildinfo.Version)
			if derr != nil {
				s.logger.Warn("app latest: dev-channel update check failed", "err", derr)
			} else {
				s.appLatest.setDevUpdate(dev)
			}
		}
		refresh()
		t := time.NewTicker(appLatestRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
}

// appReleaseTag matches a clean release image tag (vX.Y.Z) - used to pick the newest from GHCR.
var appReleaseTag = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// appVerPrefix extracts the X.Y.Z base from a running version string, which may carry a
// `git describe` suffix (e.g. "v0.4.10-3-gabc1234") on development/:testing builds.
var appVerPrefix = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)`)

// appVerDev matches the `git describe` markers a non-release build carries: a commits-ahead suffix
// ("-3-gabc1234", i.e. N commits past the nearest tag) or a "-dirty" worktree marker. Their presence
// means the build is ahead of its own tag - unreleased code, not a clean release.
var appVerDev = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+|-dirty`)

// appVerSha pulls the abbreviated commit out of a `git describe` version's "-g<sha>" marker
// (e.g. "v0.4.15-4-g1098b9e" -> "1098b9e"). This is the commit whose image is published to GHCR as
// the "sha-<short>" tag by build.yml, letting us resolve the running build's digest from the registry.
var appVerSha = regexp.MustCompile(`-g([0-9a-f]+)`)

// resolveDevChannelUpdate reports whether a newer :testing image exists for a running development
// build. It compares the digest the :testing tag currently points to against the digest of the
// running commit's own image (the sha-<short> tag), both read anonymously from public GHCR. Returns
// false (no error) for non-development builds, an un-extractable commit, or a missing sha-<short> tag
// (so a locally built image, or one whose tag GHCR doesn't have, never yields a false "update" flag).
func resolveDevChannelUpdate(ctx context.Context, cur string) (bool, error) {
	if !appVerDev.MatchString(cur) {
		return false, nil // clean release build - the release comparison already covers it
	}
	m := appVerSha.FindStringSubmatch(cur)
	if m == nil {
		return false, nil // e.g. "-dirty" with no commit to resolve
	}
	sha := m[1]

	repoPath := strings.TrimPrefix(appImageRepo, "ghcr.io/")
	tok, err := ghcrPullToken(ctx, repoPath)
	if err != nil {
		return false, err
	}
	testingDigest, err := ghcrManifestDigest(ctx, repoPath, "testing", tok)
	if err != nil {
		return false, err
	}
	runningDigest, err := ghcrManifestDigest(ctx, repoPath, "sha-"+sha, tok)
	if err != nil {
		return false, err
	}
	if testingDigest == "" || runningDigest == "" {
		return false, nil // can't compare -> don't claim an update
	}
	return testingDigest != runningDigest, nil
}

// resolveLatestAppVersion returns the newest vX.Y.Z tag published for the app image, read
// anonymously from the public GHCR registry (token -> tags list). "" if none is found.
func resolveLatestAppVersion(ctx context.Context) (string, error) {
	repoPath := strings.TrimPrefix(appImageRepo, "ghcr.io/") // "<owner>/argus"

	var tok struct {
		Token string `json:"token"`
	}
	if err := ghcrGetJSON(ctx, "https://ghcr.io/token?scope=repository:"+repoPath+":pull", "", &tok); err != nil {
		return "", err
	}
	tags, err := ghcrListTags(ctx, repoPath, tok.Token)
	if err != nil {
		return "", err
	}

	best := ""
	var bestKey [4]int
	for _, t := range tags {
		m := appReleaseTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		k := verKey(m)
		if best == "" || versionLess(bestKey, k) {
			best, bestKey = t, k
		}
	}
	return best, nil
}

// resolveReleaseNotes fetches the release body (the CHANGELOG section published as the GitHub Release
// notes) for a given tag, read anonymously from the public GitHub API. "" (no error) if the release
// has no body or is not found; errors only on transport/HTTP failures worth logging.
func resolveReleaseNotes(ctx context.Context, tag string) (string, error) {
	var rel struct {
		Body string `json:"body"`
	}
	url := "https://api.github.com/repos/" + appGitHubRepo + "/releases/tags/" + tag
	if err := ghcrGetJSON(ctx, url, "", &rel); err != nil {
		// A missing release (404) is not an error worth surfacing - notes stay empty.
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(rel.Body), nil
}

// verKey turns an appReleaseTag/appVerPrefix submatch ([full, major, minor, patch]) into a
// comparable (major, minor, patch, 0) tuple for versionLess.
func verKey(m []string) [4]int {
	var k [4]int
	for i := 0; i < 3; i++ {
		k[i], _ = strconv.Atoi(m[i+1])
	}
	return k
}

type versionResponse struct {
	Version         string `json:"version"`             // running build (git describe), "" if un-stamped
	Latest          string `json:"latest,omitempty"`    // newest published release, "" until resolved
	UpdateAvailable bool   `json:"update_available"`
	DevUpdate       bool   `json:"dev_update,omitempty"` // a newer :testing image exists for this dev build
	Status          string `json:"status"`               // current | development | outdated | dev | unknown
}

// appUpdateStatus compares the running version against the newest published release and returns a
// verdict:
//   - "dev"         - the running build has no semver base (un-stamped or a bare SHA);
//   - "unknown"     - the newest release has not been resolved from GHCR yet;
//   - "outdated"    - the running base is older than the newest release (update available);
//   - "development" - the running build is ahead of the newest release: either a `git describe`
//     build past its own tag (e.g. "v0.4.10-3-gabc1234", a :testing/main build) or a base newer
//     than anything published. Unreleased code, so NOT flagged as the latest release;
//   - "current"     - a clean release tag equal to the newest published release.
//
// Only the X.Y.Z base is compared for ordering; the `git describe` suffix (appVerDev) distinguishes
// "exactly the newest release" (current) from "one commit past it" (development).
func appUpdateStatus(cur, latest string) (status string, updateAvailable bool) {
	mc := appVerPrefix.FindStringSubmatch(cur)
	switch {
	case mc == nil:
		return "dev", false
	case latest == "":
		return "unknown", false
	}
	ml := appVerPrefix.FindStringSubmatch(latest)
	if ml == nil {
		return "unknown", false
	}
	kc, kl := verKey(mc), verKey(ml)
	switch {
	case versionLess(kc, kl):
		return "outdated", true
	case versionLess(kl, kc) || appVerDev.MatchString(cur):
		return "development", false
	default:
		return "current", false
	}
}

// handleVersion reports the running version and whether a newer release has been published, so the
// UI can show at a glance whether this instance is up to date. Authenticated (shown in the shell).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	cur := buildinfo.Version
	latest := s.appLatest.get()
	resp := versionResponse{Version: cur, Latest: latest}
	resp.Status, resp.UpdateAvailable = appUpdateStatus(cur, latest)
	// A development (:testing) build reads as "development" against releases; if a newer :testing
	// image has been published, surface that as an available update too.
	if resp.Status == "development" && s.appLatest.getDevUpdate() {
		resp.DevUpdate = true
		resp.UpdateAvailable = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVersionNotes returns the newest published release's version and its notes (the CHANGELOG
// section from the GitHub Release), so the UI can show "what's new" when an update is available.
// Authenticated; the notes are cached and refreshed alongside the latest-version poll.
func (s *Server) handleVersionNotes(w http.ResponseWriter, _ *http.Request) {
	version, notes := s.appLatest.getNotes()
	writeJSON(w, http.StatusOK, map[string]string{"version": version, "notes": notes})
}
