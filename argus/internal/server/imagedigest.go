// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Every update the core hands out (a fleet target, a one-shot probe or sidecar update, its own
// self-update request) names a tag. Tags stay what people use (latest, testing, a version), but a
// tag is a pointer the registry can move: the core therefore also hands out the digest the tag
// resolved to at hand-out time, and the updater refuses to run an image whose pulled digest differs.
// A tag swapped in the registry between the hand-out and the pull is refused rather than applied.

// digestTTL is how long a resolved tag -> digest is reused before GHCR is asked again. Check-ins
// arrive once a minute per probe; the registry is asked once per tag per window.
const digestTTL = 5 * time.Minute

// digestShape is what a content digest looks like; the updater checks the same shape.
var digestShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type digestCache struct {
	mu      sync.Mutex
	entries map[string]digestEntry
}

type digestEntry struct {
	digest string
	at     time.Time
}

// imageDigest returns the digest repo:tag currently points to, or "" when it can't be resolved
// (the registry unreachable, the tag absent). A "" digest means the updater applies the tag without
// the check, as before; the log says so.
func (s *Server) imageDigest(ctx context.Context, repo, tag string) string {
	key := repo + ":" + tag
	s.digests.mu.Lock()
	if e, ok := s.digests.entries[key]; ok && time.Since(e.at) < digestTTL {
		s.digests.mu.Unlock()
		return e.digest
	}
	s.digests.mu.Unlock()

	repoPath := strings.TrimPrefix(repo, "ghcr.io/")
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	digest := ""
	tok, err := ghcrPullToken(c, repoPath)
	if err == nil {
		digest, err = ghcrManifestDigest(c, repoPath, tag, tok)
	}
	if err != nil || !digestShape.MatchString(digest) {
		if err != nil {
			s.logger.Warn("image digest: could not resolve; the update will be applied unverified", "image", key, "err", err)
		}
		digest = ""
	}
	s.digests.mu.Lock()
	if s.digests.entries == nil {
		s.digests.entries = map[string]digestEntry{}
	}
	// A failed resolve is remembered for a minute only, so a registry blip doesn't leave a whole
	// window of hand-outs unverified.
	at := time.Now()
	if digest == "" {
		at = at.Add(-digestTTL + time.Minute)
	}
	s.digests.entries[key] = digestEntry{digest: digest, at: at}
	s.digests.mu.Unlock()
	return digest
}
