// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

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

// --- probe appliance (VM) images, resolved from the argus-probe GitHub Releases -------------------

// probeVMImage is one downloadable appliance file of the newest probe-vm release (OVA/qcow2/VHD).
type probeVMImage struct {
	Name  string `json:"name"`  // asset filename, e.g. argus-probe-vm.ova
	Label string `json:"label"` // friendly format label for the button
	URL   string `json:"url"`   // direct browser download URL (GitHub release asset)
	Size  int64  `json:"size"`  // bytes
}

// probeVMInfo is the newest probe-vm appliance release and its downloadable images.
type probeVMInfo struct {
	Version string         `json:"version"` // e.g. "v0.3.2" (the "probe-vm/" prefix stripped)
	Page    string         `json:"page"`    // the release page URL (fallback link when no assets)
	Images  []probeVMImage `json:"images"`
}

// probeVMCache holds the newest probe-vm release info, resolved from the public GitHub Releases API
// and refreshed periodically, so the Add-probe wizard can offer direct appliance downloads instead of
// sending the user to GitHub to find them.
type probeVMCache struct {
	mu   sync.RWMutex
	info probeVMInfo
}

func (c *probeVMCache) get() probeVMInfo  { c.mu.RLock(); defer c.mu.RUnlock(); return c.info }
func (c *probeVMCache) set(v probeVMInfo) { c.mu.Lock(); c.info = v; c.mu.Unlock() }

// startProbeVMRefresh polls the argus-probe GitHub Releases for the newest probe-vm appliance and its
// download assets, same cadence + best-effort semantics as the GHCR polls (an empty cache just means
// the wizard falls back to a "releases page" link).
func (s *Server) startProbeVMRefresh(ctx context.Context) {
	go func() {
		refresh := func() {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			info, err := resolveLatestProbeVM(c)
			if err != nil {
				s.logger.Warn("probe-vm images: GitHub resolve failed", "err", err)
				return
			}
			if info.Version != "" {
				s.probeVM.set(info)
				s.logger.Info("probe-vm images resolved from GitHub", "version", info.Version, "images", len(info.Images))
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

var probeVMRelTag = regexp.MustCompile(`^probe-vm/v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// vmImageLabel maps an appliance filename to a friendly "format — hypervisor" label.
func vmImageLabel(name string) string {
	switch {
	case strings.HasSuffix(name, ".ova"):
		return "OVA · VirtualBox / VMware"
	case strings.HasSuffix(name, ".qcow2"):
		return "qcow2 · KVM / QEMU / Proxmox"
	case strings.HasSuffix(name, ".vhd.gz"), strings.HasSuffix(name, ".vhd"):
		return "VHD · Hyper-V"
	default:
		return name
	}
}

// isVMImageAsset reports whether a release asset is a downloadable appliance image (not a checksum or
// other sidecar file).
func isVMImageAsset(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".ova") || strings.HasSuffix(n, ".qcow2") ||
		strings.HasSuffix(n, ".vhd") || strings.HasSuffix(n, ".vhd.gz")
}

// resolveLatestProbeVM returns the newest probe-vm/vX.Y.Z release of the argus-probe repo and its
// appliance download assets, read anonymously from the public GitHub Releases API.
func resolveLatestProbeVM(ctx context.Context) (probeVMInfo, error) {
	repoPath := strings.TrimPrefix(probeImageRepo, "ghcr.io/") // "<owner>/argus-probe"
	var rels []struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := githubGetJSON(ctx, "https://api.github.com/repos/"+repoPath+"/releases?per_page=100", &rels); err != nil {
		return probeVMInfo{}, err
	}
	best := ""
	var bestKey [4]int
	var out probeVMInfo
	for _, r := range rels {
		if r.Draft {
			continue
		}
		m := probeVMRelTag.FindStringSubmatch(r.TagName)
		if m == nil {
			continue
		}
		var k [4]int
		for i := 0; i < 3; i++ {
			k[i], _ = strconv.Atoi(m[i+1])
		}
		if best != "" && !versionLess(bestKey, k) { // not newer than the best so far
			continue
		}
		imgs := make([]probeVMImage, 0, len(r.Assets))
		for _, a := range r.Assets {
			if isVMImageAsset(a.Name) {
				imgs = append(imgs, probeVMImage{Name: a.Name, Label: vmImageLabel(strings.ToLower(a.Name)), URL: a.URL, Size: a.Size})
			}
		}
		best, bestKey = r.TagName, k
		out = probeVMInfo{Version: strings.TrimPrefix(r.TagName, "probe-vm/"), Page: r.HTMLURL, Images: imgs}
	}
	return out, nil
}

// handleProbeVMImages returns the newest probe appliance (VM) release and its downloadable images, so
// the Add-probe wizard can offer direct OVA/qcow2/VHD downloads. Empty until the first GitHub resolve
// (or if it fails) - the UI then falls back to a link to the releases page.
func (s *Server) handleProbeVMImages(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.probeVM.get())
}

// githubGetJSON GETs a public GitHub API endpoint and decodes the JSON. GitHub requires a User-Agent.
func githubGetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "argus")
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

// updaterImageRepo is the public GHCR repository for the argus-updater sidecar image (semver-tagged
// X.Y.Z, unlike the probe image's X.Y.Z-rN). Used to resolve the newest updater version for drift.
const updaterImageRepo = "ghcr.io/g-guglielmi/argus-updater"

// startUpdaterLatestRefresh polls GHCR for the newest published argus-updater version, so the Probes
// view can flag whether each probe's updater sidecar is up to date (like it does for the proxy). Same
// cadence + best-effort semantics as the probe poll.
func (s *Server) startUpdaterLatestRefresh(ctx context.Context) {
	go func() {
		refresh := func() {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			v, err := resolveLatestUpdaterVersion(c)
			if err != nil {
				s.logger.Warn("updater latest: GHCR resolve failed", "err", err)
				return
			}
			if v != "" {
				s.updaterLatest.set(v)
				s.logger.Info("updater latest resolved from GHCR", "version", v)
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

var updaterVerTag = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// resolveLatestUpdaterVersion returns the newest X.Y.Z tag published for the argus-updater image, read
// anonymously from public GHCR. "" if none found.
func resolveLatestUpdaterVersion(ctx context.Context) (string, error) {
	repoPath := strings.TrimPrefix(updaterImageRepo, "ghcr.io/")
	tok, err := ghcrPullToken(ctx, repoPath)
	if err != nil {
		return "", err
	}
	tags, err := ghcrListTags(ctx, repoPath, tok)
	if err != nil {
		return "", err
	}
	best := ""
	var bestKey [4]int
	for _, t := range tags {
		m := updaterVerTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		var k [4]int
		for i := 0; i < 3; i++ {
			k[i], _ = strconv.Atoi(m[i+1])
		}
		if best == "" || versionLess(bestKey, k) {
			best, bestKey = t, k
		}
	}
	return best, nil
}

// updaterStatus classifies a probe's reported updater-sidecar version against the newest published
// updater RELEASE tag (updaterLatest only resolves X.Y.Z tags, not :latest digests). "unknown" when
// either is unknown, "outdated" only when the reported X.Y.Z base is genuinely OLDER than the newest
// release, else "current".
//
// The comparison is on the semver BASE, not the whole string: a sidecar tracking :latest reports a
// git-describe build ("v0.2.3-3-gabc123") whose base equals the newest release, and a plain string
// compare flagged it "outdated → 0.2.3" forever - a phantom "update available" that clicking Update
// could never clear (the next :latest is still a describe build, still != the clean tag), and which
// disagreed with a digest-based check (Dockhand). Digest drift on :latest is the updater's own
// self-update job; Argus's tag view only answers "is it behind the newest release?" - so a describe
// build at or past the newest tag is "current", same policy as the core's own appUpdateStatus.
func updaterStatus(reported, latest string) string {
	r := strings.TrimPrefix(strings.TrimSpace(reported), "v")
	l := strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if r == "" || l == "" {
		return "unknown"
	}
	mr := appVerPrefix.FindStringSubmatch(r)
	ml := appVerPrefix.FindStringSubmatch(l)
	if mr == nil || ml == nil {
		// One side has no X.Y.Z base to compare - fall back to exact match.
		if r == l {
			return "current"
		}
		return "outdated"
	}
	if versionLess(verKey(mr), verKey(ml)) { // reported base older than the newest release
		return "outdated"
	}
	return "current" // equal, or a development build at/past the newest release
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

// ghcrManifestAccept is the media-type set to request when GETting a manifest or index.
const ghcrManifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

// ghcrGetManifestJSON GETs a manifest/index by ref-or-digest and decodes it, sending the manifest
// Accept header (GHCR returns the config-blob JSON for the wrong Accept otherwise).
func ghcrGetManifestJSON(ctx context.Context, repoPath, ref, bearer string, out any) error {
	u := "https://ghcr.io/v2/" + repoPath + "/manifests/" + ref
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", ghcrManifestAccept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ghcrImageLabels reads the OCI config labels off the image a tag (or digest) points to. It drills
// index -> image manifest -> config blob, skipping the attestation manifest that buildx provenance
// adds (platform "unknown"). Returns an empty (non-nil) map when the image carries no labels. Callers
// pick out individual labels (org.opencontainers.image.version / .revision).
func ghcrImageLabels(ctx context.Context, repoPath, ref, bearer string) (map[string]string, error) {
	var top struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"` // present on an image manifest
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS   string `json:"os"`
				Arch string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"` // present on an index
	}
	if err := ghcrGetManifestJSON(ctx, repoPath, ref, bearer, &top); err != nil {
		return nil, err
	}
	configDigest := top.Config.Digest
	if configDigest == "" && len(top.Manifests) > 0 {
		target := ""
		for _, m := range top.Manifests {
			if m.Platform.OS != "" && m.Platform.OS != "unknown" { // the real image, not the attestation
				target = m.Digest
				break
			}
		}
		if target == "" {
			target = top.Manifests[0].Digest
		}
		var img struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if err := ghcrGetManifestJSON(ctx, repoPath, target, bearer, &img); err != nil {
			return nil, err
		}
		configDigest = img.Config.Digest
	}
	if configDigest == "" {
		return map[string]string{}, nil
	}
	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := ghcrGetJSON(ctx, "https://ghcr.io/v2/"+repoPath+"/blobs/"+configDigest, bearer, &cfg); err != nil {
		return nil, err
	}
	if cfg.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return cfg.Config.Labels, nil
}

// ghcrImageVersionLabel reads the org.opencontainers.image.version label off the image a tag points
// to. Empty (no error) if the label is absent (e.g. an image built before the label was added).
func ghcrImageVersionLabel(ctx context.Context, repoPath, ref, bearer string) (string, error) {
	labels, err := ghcrImageLabels(ctx, repoPath, ref, bearer)
	if err != nil {
		return "", err
	}
	return labels["org.opencontainers.image.version"], nil
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
