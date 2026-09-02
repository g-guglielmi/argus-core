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
const appGitHubRepo = "g-guglielmi/argus-core"

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

	// Testing-channel check: only a box that tracks :testing is offered newer :testing builds; a clean
	// release on :latest keeps to the release comparison above. This must be gated on the channel
	// because a clean release image is byte-identical on :latest and :testing, so right after a release
	// the running version alone can't tell them apart (see resolveChannel).
	if s.resolveChannel() == "testing" {
		dev, target, derr := resolveTestingUpdate(c, buildinfo.Version)
		if derr != nil {
			s.logger.Warn("app latest: testing-channel update check failed", "err", derr)
		} else {
			s.appLatest.setDevUpdate(dev, target)
		}
	} else {
		s.appLatest.setDevUpdate(false, "")
	}
}

// resolveChannel returns the release channel this instance tracks: "testing" or "latest". A clean
// release image is byte-identical on :latest and :testing, so the running version alone can't tell the
// channels apart right after a release. The authoritative signal is the tag the core container is
// actually running under, which the argus-updater sidecar (it holds the Docker socket) reports into
// the shared update dir; a :testing tag means the testing channel, anything else tracks the release
// line. Without a sidecar report we fall back to the version stamp: a dev-stamped build only ever comes
// from :testing, otherwise assume latest (the safe default, so a :latest box is never offered testing).
func (s *Server) resolveChannel() string {
	tag, ok := s.reportedCoreTag()
	return channelFor(buildinfo.Version, tag, ok)
}

// channelFor is the pure channel-resolution logic behind resolveChannel (extracted so it can be
// unit-tested without a running container / sidecar). A dev-stamped version is checked FIRST and wins:
// a build ahead of the newest release can only come from :testing (a clean :latest release is never
// dev-stamped), so it's authoritatively the testing channel whatever tag a sidecar happens to report.
// The sidecar tag only disambiguates a CLEAN build, which is byte-identical on :latest and :testing and
// so can't be told apart from its version alone.
func channelFor(version, reportedTag string, hasReport bool) string {
	if appVerDev.MatchString(version) {
		return "testing"
	}
	if hasReport {
		if reportedTag == "testing" {
			return "testing"
		}
		return "latest"
	}
	return "latest"
}

// coreImageFile is where the argus-updater sidecar records the tag the core container runs under.
const coreImageFile = "core-image.json"

// reportedCoreTag returns the image tag the sidecar last reported for the core container (e.g.
// "testing", "latest", "v0.4.18"), and whether a report was available. Only meaningful when the
// self-update channel (shared dir) is wired; absent/unreadable yields ("", false).
func (s *Server) reportedCoreTag() (string, bool) {
	if !s.cfg.SelfUpdateEnabled() {
		return "", false
	}
	var rec struct {
		Tag string `json:"tag"`
	}
	ok, err := readUpdateJSON(s.updatePath(coreImageFile), &rec)
	if err != nil || !ok || rec.Tag == "" {
		return "", false
	}
	return rec.Tag, true
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

// runningImageRef returns the GHCR tag that identifies the running build's own image, so its digest
// can be compared with the :testing tag's: the sha-<short> tag for a dev build ("-N-gsha"), or the
// clean vX.Y.Z release tag for a clean build (which build.yml also publishes). "" when the build can't
// be identified (un-stamped, or "-dirty" with no commit) - the caller then reports no update.
func runningImageRef(cur string) string {
	if m := appVerSha.FindStringSubmatch(cur); m != nil {
		return "sha-" + m[1]
	}
	if appReleaseTag.MatchString(cur) {
		return cur
	}
	return ""
}

// resolveTestingUpdate reports whether a newer :testing image exists for a box tracking :testing. It
// compares the digest the :testing tag currently points to against the running image's own digest,
// both read anonymously from public GHCR. The running image is identified by whichever tag build.yml
// published it under: the sha-<short> tag for a dev build ("-N-gsha"), or the clean vX.Y.Z release tag
// for a clean build - which is what a just-released :testing box runs until it next updates, and the
// exact case where the running version alone can't tell :latest from :testing. Different digests mean
// an update; target is the :testing image's OCI version label (best-effort, "" if unreadable). Returns
// (false, "") when the running image can't be identified (un-stamped, or "-dirty" with no sha), so a
// locally built image never yields a false "update".
func resolveTestingUpdate(ctx context.Context, cur string) (available bool, target string, err error) {
	runningRef := runningImageRef(cur)
	if runningRef == "" {
		return false, "", nil // can't identify the running image
	}

	repoPath := strings.TrimPrefix(appImageRepo, "ghcr.io/")
	tok, err := ghcrPullToken(ctx, repoPath)
	if err != nil {
		return false, "", err
	}
	testingDigest, err := ghcrManifestDigest(ctx, repoPath, "testing", tok)
	if err != nil {
		return false, "", err
	}
	runningDigest, err := ghcrManifestDigest(ctx, repoPath, runningRef, tok)
	if err != nil {
		return false, "", err
	}
	if testingDigest == "" || runningDigest == "" || testingDigest == runningDigest {
		return false, "", nil // same image (or can't compare) -> no update
	}
	// Digests differ - but that alone is NOT a real update. The same commit rebuilt (a release tag
	// build and a main-push build of the identical commit; or any non-reproducible rebuild) yields a
	// different digest while carrying the same code. Comparing the git revision the two images were
	// built from (the org.opencontainers.image.revision label build.yml stamps) tells same-code apart
	// from newer-code: equal revisions -> same source -> no update, which stops a just-released box
	// from being offered a phantom ":testing" update to its own commit forever. If either revision is
	// unreadable (an image built before the label existed) we fall back to the digest verdict.
	if sameRevision(ctx, repoPath, tok, runningRef) {
		return false, "", nil
	}
	// An update exists; best-effort read the target build's version label to name it (empty is fine).
	target, lerr := ghcrImageVersionLabel(ctx, repoPath, "testing", tok)
	if lerr != nil {
		target = ""
	}
	return true, target, nil
}

// ociRevisionLabel is the OCI label build.yml stamps with the git commit an image was built from.
const ociRevisionLabel = "org.opencontainers.image.revision"

// sameRevision reports whether the :testing image and the running image (identified by runningRef)
// were built from the same git commit. Used to distinguish a same-commit rebuild (different digest,
// identical code) from a genuinely newer :testing build. Returns false when either revision label is
// missing or unreadable, so a real update is never suppressed on incomplete data.
func sameRevision(ctx context.Context, repoPath, tok, runningRef string) bool {
	testingLabels, err := ghcrImageLabels(ctx, repoPath, "testing", tok)
	if err != nil {
		return false
	}
	runningLabels, err := ghcrImageLabels(ctx, repoPath, runningRef, tok)
	if err != nil {
		return false
	}
	return revisionsMatch(testingLabels[ociRevisionLabel], runningLabels[ociRevisionLabel])
}

// revisionsMatch reports whether two image git-revision labels identify the same commit. Both must be
// present and equal; a missing revision (empty) yields false so an update is never suppressed when the
// build source can't be established.
func revisionsMatch(testingRev, runningRev string) bool {
	return testingRev != "" && runningRev != "" && testingRev == runningRev
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

// devUpdateOffered reports whether a newer :testing image should be offered to this box: the cache
// flags one (devUpd) AND the running build is either a development build or a clean release ("current")
// that tracks the testing channel (the just-released/"aligned" box). Both handleVersion (which shows the
// button) and handleUpdateStart (which acts on the click) use this predicate so they never disagree -
// gating the action on status=="development" alone silently refused the update on a clean-tag testing box.
func devUpdateOffered(status string, devUpd bool) bool {
	return devUpd && (status == "development" || status == "current")
}

// handleVersion reports the running version and whether a newer release has been published, so the
// UI can show at a glance whether this instance is up to date. Authenticated (shown in the shell).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	cur := buildinfo.Version
	latest := s.appLatest.get()
	resp := versionResponse{Version: cur, Latest: latest}
	resp.Status, resp.UpdateAvailable = appUpdateStatus(cur, latest)
	// A box tracking :testing is offered the newer :testing image when one exists. This also covers the
	// "aligned" case: right after a release the running build is a clean vX.Y.Z (so appUpdateStatus says
	// "current"), yet :testing has since advanced - the testing check flags it, and we flip the verdict
	// to "development" so the UI shows the dev pill and names the target instead of a green LATEST badge.
	if devUpd, target := s.appLatest.getDevUpdate(); devUpdateOffered(resp.Status, devUpd) {
		resp.DevUpdate = true
		resp.UpdateAvailable = true
		resp.DevTarget = target
		resp.Status = "development"
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
