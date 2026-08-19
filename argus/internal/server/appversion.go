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

// appLatestRefresh is how often we re-poll GHCR for the newest published release.
const appLatestRefresh = 3 * time.Hour

// appLatestCache holds the newest published app release (vX.Y.Z), resolved anonymously from the
// public GHCR tags list and refreshed periodically, so the UI can flag "update available" instead
// of guessing.
type appLatestCache struct {
	mu      sync.RWMutex
	version string
}

func (c *appLatestCache) get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

func (c *appLatestCache) set(v string) {
	c.mu.Lock()
	c.version = v
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
			if v != "" {
				s.appLatest.set(v)
				s.logger.Info("app latest resolved from GHCR", "version", v)
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
// `git describe` suffix (e.g. "v0.4.10-3-gabc1234") on development/:latest builds.
var appVerPrefix = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)`)

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
	Version         string `json:"version"`          // running build (git describe), "" if un-stamped
	Latest          string `json:"latest,omitempty"` // newest published release, "" until resolved
	UpdateAvailable bool   `json:"update_available"`
	Status          string `json:"status"` // current | outdated | dev | unknown
}

// appUpdateStatus compares the running version against the newest published release and returns a
// verdict: "dev" (running build has no semver base - un-stamped or a bare SHA), "unknown" (latest
// not resolved yet), "outdated" (a newer release exists, update available), or "current" (on the
// newest release, or a :latest/dev build ahead of it). Only the X.Y.Z base is compared, so a
// `git describe` build like "v0.4.10-3-gabc1234" reads as current when the last release is 0.4.10.
func appUpdateStatus(cur, latest string) (status string, updateAvailable bool) {
	mc := appVerPrefix.FindStringSubmatch(cur)
	switch {
	case mc == nil:
		return "dev", false
	case latest == "":
		return "unknown", false
	default:
		ml := appVerPrefix.FindStringSubmatch(latest)
		if ml != nil && versionLess(verKey(mc), verKey(ml)) {
			return "outdated", true
		}
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
	writeJSON(w, http.StatusOK, resp)
}
