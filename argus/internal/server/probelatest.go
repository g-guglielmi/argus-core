package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// probeImageRepo is the public GHCR repository for the self-enrolling probe image. Kept in step
// with the frontend PROBE_IMAGE constant; a fork changes both.
const probeImageRepo = "ghcr.io/g-guglielmi/argus-probe"

// probeLatestRefresh is how often we re-poll GHCR for the newest published probe revision.
const probeLatestRefresh = 3 * time.Hour

// probeLatestCache holds the newest published probe version (X.Y.Z-rN), resolved anonymously from
// the public GHCR tags list and refreshed periodically. It lets the fleet view compute real drift
// against the "latest" target instead of only showing "tracking".
type probeLatestCache struct {
	mu      sync.RWMutex
	version string
}

func (c *probeLatestCache) get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

func (c *probeLatestCache) set(v string) {
	c.mu.Lock()
	c.version = v
	c.mu.Unlock()
}

// startProbeLatestRefresh kicks off a background poller: an immediate resolve, then every few hours.
// Non-blocking (server startup never waits on GHCR); failures leave the cache empty and the fleet
// view falls back to "tracking latest".
func (s *Server) startProbeLatestRefresh(ctx context.Context) {
	go func() {
		refresh := func() {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			v, err := resolveLatestProbeVersion(c)
			if err != nil {
				s.logger.Warn("probe latest: GHCR resolve failed", "err", err)
				return
			}
			if v != "" {
				s.probeLatest.set(v)
				s.logger.Info("probe latest resolved from GHCR", "version", v)
			}
		}
		refresh()
		t := time.NewTicker(probeLatestRefresh)
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

var probeVerTag = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)-r([0-9]+)$`)

// resolveLatestProbeVersion returns the newest X.Y.Z-rN tag published for the probe image, read
// anonymously from the public GHCR registry (token -> tags list). "" if none is found.
func resolveLatestProbeVersion(ctx context.Context) (string, error) {
	repoPath := strings.TrimPrefix(probeImageRepo, "ghcr.io/") // "<owner>/argus-probe"

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
		m := probeVerTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		var k [4]int
		for i := 0; i < 4; i++ {
			k[i], _ = strconv.Atoi(m[i+1])
		}
		if best == "" || versionLess(bestKey, k) {
			best, bestKey = t, k
		}
	}
	return best, nil
}

// versionLess reports whether a sorts before b as a (major, minor, patch, revision) tuple.
func versionLess(a, b [4]int) bool {
	for i := 0; i < 4; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ghcrListTags returns every tag for a repo, following the registry's Link-header pagination. The
// tags/list endpoint pages (100 by default, and honours ?n= up to a server cap), so a single GET
// silently misses the newest tags once a repo has more than one page - which would make "latest"
// resolve to a stale older version. Cursor is the last tag of each page (standard registry paging).
func ghcrListTags(ctx context.Context, repoPath, bearer string) ([]string, error) {
	base := "https://ghcr.io/v2/" + repoPath + "/tags/list"
	var all []string
	last := ""
	for {
		u := base + "?n=1000"
		if last != "" {
			u += "&last=" + url.QueryEscape(last)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
		var page struct {
			Tags []string `json:"tags"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&page)
		hasNext := strings.Contains(resp.Header.Get("Link"), `rel="next"`)
		resp.Body.Close()
		if derr != nil {
			return nil, derr
		}
		all = append(all, page.Tags...)
		if !hasNext || len(page.Tags) == 0 {
			return all, nil
		}
		last = page.Tags[len(page.Tags)-1]
	}
}

// ghcrPullToken fetches an anonymous pull-scoped bearer token for a public GHCR repo.
func ghcrPullToken(ctx context.Context, repoPath string) (string, error) {
	var tok struct {
		Token string `json:"token"`
	}
	if err := ghcrGetJSON(ctx, "https://ghcr.io/token?scope=repository:"+repoPath+":pull", "", &tok); err != nil {
		return "", err
	}
	return tok.Token, nil
}

// ghcrManifestDigest returns the content digest a tag currently points to (the registry's
// Docker-Content-Digest header), so two tags can be compared for "same image or not" without pulling.
// A 404 (tag absent) returns ("", nil) so callers can treat "no such tag" as "can't compare" rather
// than an error. Accepts OCI/Docker manifest + index media types (multi-arch tags resolve to their
// index digest, which is stable for the comparison).
func ghcrManifestDigest(ctx context.Context, repoPath, ref, bearer string) (string, error) {
	u := "https://ghcr.io/v2/" + repoPath + "/manifests/" + ref
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

func ghcrGetJSON(ctx context.Context, url, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
