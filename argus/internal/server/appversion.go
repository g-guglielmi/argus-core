package server

import (
	"context"
	"net/http"
	"regexp"
	"sort"
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

// appLatestHour is the local hour (in the configured timezone) at which the daily GHCR update check
// runs. A release is a rare event, so a nightly check plus the manual "Check for updates" button is
// plenty; nightly avoids hammering the registry and keeps the check off peak hours.
const appLatestHour = 4

// appLatestCache holds the newest published app release (vX.Y.Z) plus its release notes, resolved from
// public GHCR (tags) + the GitHub Releases API and refreshed periodically, so the UI can flag "update
// available" and show what changed instead of guessing.
type appLatestCache struct {
	mu        sync.RWMutex
	version   string
	notes     string   // release body (CHANGELOG section) for `version`, "" if not fetched
	devUpdate bool     // a development (:testing) build is running and a newer :testing image exists
	devTarget string   // the target :testing build's version (OCI label), "" if unknown
	releases  []string // published vX.Y.Z release tags, newest first (for the version switcher)
}

func (c *appLatestCache) getReleases() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.releases))
	copy(out, c.releases)
	return out
}

func (c *appLatestCache) setReleases(r []string) {
	c.mu.Lock()
	c.releases = r
	c.mu.Unlock()
}

func (c *appLatestCache) get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

func (c *appLatestCache) getDevUpdate() (available bool, target string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.devUpdate, c.devTarget
}

func (c *appLatestCache) setDevUpdate(available bool, target string) {
	c.mu.Lock()
	c.devUpdate, c.devTarget = available, target
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

// refreshAppLatest re-resolves the newest release (+ its notes) and the dev-channel update flag from
// GHCR, updating the cache. Shared by the nightly scheduler and the manual "Check for updates" button.
func (s *Server) refreshAppLatest(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	releases, err := resolveAppReleases(c)
	if err != nil {
		s.logger.Warn("app latest: GHCR resolve failed", "err", err)
		return
	}
	s.appLatest.setReleases(releases)
	if len(releases) > 0 {
		v := releases[0] // newest, for the "update available" verdict
		// Best-effort: fetch the release notes so the UI can show what changed. A failure here
		// (rate limit, no matching release) just leaves notes empty - the version verdict stands.
		notes, nerr := resolveReleaseNotes(c, v)
		if nerr != nil {
			s.logger.Warn("app latest: release notes fetch failed", "version", v, "err", nerr)
		}
		s.appLatest.setNotes(v, notes)
		s.logger.Info("app latest resolved from GHCR", "version", v, "releases", len(releases), "notes", notes != "")
	}

	// Development-channel check: a :testing build reads as "development" against releases (it's
	// ahead of the newest v*), so the release comparison alone never flags an update for it. Compare
	// the running commit's image digest to the current :testing digest so a box tracking :testing
	// still learns when a newer testing build has been published.
	dev, target, derr := resolveDevChannelUpdate(c, buildinfo.Version)
	if derr != nil {
		s.logger.Warn("app latest: dev-channel update check failed", "err", derr)
	} else {
		s.appLatest.setDevUpdate(dev, target)
	}
}

// nextDailyCheck returns the next occurrence of appLatestHour:00 in loc, strictly after now.
func nextDailyCheck(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), appLatestHour, 0, 0, 0, loc)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// startAppLatestRefresh kicks off the background update poller: an immediate resolve at startup, then
// once daily at appLatestHour (local time). Non-blocking (startup never waits on GHCR); failures leave
// the cache as-is and the UI shows the running version without an up-to-date verdict. The next-check
// time is recomputed each night so it tracks DST and any timezone change.
func (s *Server) startAppLatestRefresh(ctx context.Context) {
	go func() {
		s.refreshAppLatest(ctx)
		for {
			next := nextDailyCheck(time.Now(), s.mgr.Location())
			t := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				s.refreshAppLatest(ctx)
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
// running commit's own image (the sha-<short> tag), both read anonymously from public GHCR. When an
// update exists it also returns the target build's version (the :testing image's OCI version label,
// e.g. "v0.4.16-3-gabcdef1") so the UI can name the exact build; "" if the label can't be read (e.g.
// an older :testing image built before the label was added). Returns (false, "") for non-development
// builds, an un-extractable commit, or a missing sha-<short> tag, so a locally built image or one GHCR
// doesn't have never yields a false "update" flag.
func resolveDevChannelUpdate(ctx context.Context, cur string) (available bool, target string, err error) {
	if !appVerDev.MatchString(cur) {
		return false, "", nil // clean release build - the release comparison already covers it
	}
	m := appVerSha.FindStringSubmatch(cur)
	if m == nil {
		return false, "", nil // e.g. "-dirty" with no commit to resolve
	}
	sha := m[1]

	repoPath := strings.TrimPrefix(appImageRepo, "ghcr.io/")
	tok, err := ghcrPullToken(ctx, repoPath)
	if err != nil {
		return false, "", err
	}
	testingDigest, err := ghcrManifestDigest(ctx, repoPath, "testing", tok)
	if err != nil {
		return false, "", err
	}
	runningDigest, err := ghcrManifestDigest(ctx, repoPath, "sha-"+sha, tok)
	if err != nil {
		return false, "", err
	}
	if testingDigest == "" || runningDigest == "" || testingDigest == runningDigest {
		return false, "", nil // same image (or can't compare) -> no update
	}
	// An update exists; best-effort read the target build's version label to name it (empty is fine).
	target, lerr := ghcrImageVersionLabel(ctx, repoPath, "testing", tok)
	if lerr != nil {
		target = ""
	}
	return true, target, nil
}

// resolveAppReleases returns every published vX.Y.Z release tag for the app image, newest first, read
// anonymously from the public GHCR registry (token -> tags list). Empty if none are found.
func resolveAppReleases(ctx context.Context) ([]string, error) {
	repoPath := strings.TrimPrefix(appImageRepo, "ghcr.io/") // "<owner>/argus"
	tok, err := ghcrPullToken(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	tags, err := ghcrListTags(ctx, repoPath, tok)
	if err != nil {
		return nil, err
	}
	type rel struct {
		tag string
		key [4]int
	}
	var rels []rel
	for _, t := range tags {
		m := appReleaseTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		rels = append(rels, rel{t, verKey(m)})
	}
	sort.Slice(rels, func(i, j int) bool { return versionLess(rels[j].key, rels[i].key) }) // desc
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.tag
	}
	return out, nil
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
	Version         string `json:"version"`              // running build (git describe), "" if un-stamped
	Latest          string `json:"latest,omitempty"`     // newest published release, "" until resolved
	UpdateAvailable bool   `json:"update_available"`
	DevUpdate       bool   `json:"dev_update,omitempty"`  // a newer :testing image exists for this dev build
	DevTarget       string `json:"dev_target,omitempty"`  // the target :testing build's version, when known
	Status          string `json:"status"`                // current | development | outdated | dev | unknown
}

// appUpdateStatus compares the running version against the newest published release and returns a
// verdict:
//   - "dev"         - the running build has no semver base (un-stamped or a bare SHA);
//   - "development" - the running build carries a `git describe` suffix (e.g. "v0.4.15-9-gabc1234",
//     a :testing/main build) or -dirty: unreleased code. Its numeric base is NOT compared against
//     the newest release, because on our model :testing == main tip (its code is at least the newest
//     release's), and an in-place update on a rolling channel can't converge on a clean release tag
//     anyway. Whether a *newer testing image* exists is decided separately by the digest-based
//     dev-channel check (dev_update), not here - so this never offers an unreachable release update;
//   - "unknown"     - the newest release has not been resolved from GHCR yet (clean build only);
//   - "outdated"    - a clean release build older than the newest release (update available);
//   - "current"     - a clean release tag equal to the newest published release.
func appUpdateStatus(cur, latest string) (status string, updateAvailable bool) {
	mc := appVerPrefix.FindStringSubmatch(cur)
	if mc == nil {
		return "dev", false
	}
	// A development build (git-describe suffix or -dirty) is unreleased code; classify it as such up
	// front so a base-vs-release comparison never mislabels it "outdated" and offers a release update
	// it can't reach in place. Its update signal is the digest-based dev-channel check.
	if appVerDev.MatchString(cur) {
		return "development", false
	}
	if latest == "" {
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
	case versionLess(kl, kc):
		// A clean release build whose base is newer than anything published (e.g. a hand-built
		// v1.0.0 ahead of the newest release) - unreleased, so not "current".
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
	// image has been published, surface that as an available update too (and name the target build).
	if devUpd, target := s.appLatest.getDevUpdate(); resp.Status == "development" && devUpd {
		resp.DevUpdate = true
		resp.UpdateAvailable = true
		resp.DevTarget = target
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVersionCheck forces an immediate GHCR re-check (instead of waiting for the nightly poll) and
// returns the refreshed version verdict, so an admin can pull in a just-published build on demand.
func (s *Server) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	s.refreshAppLatest(r.Context())
	s.handleVersion(w, r)
}

// appTargetReleases is how many recent releases the version switcher offers (plus the two channels).
const appTargetReleases = 5

// updateTargets is the set of tags an admin may deliberately switch the core to: the rolling channels
// plus the most recent releases.
func (s *Server) updateTargets() (channels, releases []string) {
	rels := s.appLatest.getReleases()
	if len(rels) > appTargetReleases {
		rels = rels[:appTargetReleases]
	}
	return []string{"latest", "testing"}, rels
}

// isAllowedTarget reports whether tag is one of the selectable switch targets (guards against an
// admin POSTing an arbitrary tag).
func (s *Server) isAllowedTarget(tag string) bool {
	channels, releases := s.updateTargets()
	for _, t := range append(channels, releases...) {
		if t == tag {
			return true
		}
	}
	return false
}

// handleVersionTags lists the channels + recent releases the core can be switched to (for the
// version/channel picker in Settings). Authenticated.
func (s *Server) handleVersionTags(w http.ResponseWriter, _ *http.Request) {
	channels, releases := s.updateTargets()
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels, "releases": releases})
}

// handleVersionNotes returns the newest published release's version and its notes (the CHANGELOG
// section from the GitHub Release), so the UI can show "what's new" when an update is available.
// Authenticated; the notes are cached and refreshed alongside the latest-version poll.
func (s *Server) handleVersionNotes(w http.ResponseWriter, _ *http.Request) {
	version, notes := s.appLatest.getNotes()
	writeJSON(w, http.StatusOK, map[string]string{"version": version, "notes": notes})
}
