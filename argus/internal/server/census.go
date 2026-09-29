// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
)

// The sensor census (every curated sensor with its single state) costs a read of every item,
// every problem and their triggers, so it is built on the server in the background and served
// from memory: the status pills, the Overview and the status pages answer at once instead of
// waiting for Zabbix, and every open browser shares one build instead of starting its own.
//
// Freshness: while someone has looked at it in the last few minutes, the census is rebuilt every
// censusActiveEvery; otherwise every censusIdleEvery. A change a user makes through Argus (an
// acknowledgement, a pause, a threshold) bumps the generation, and the next read waits for a
// build of the new generation, so the person who acted sees the result of the action.
const (
	censusActiveEvery = 20 * time.Second
	censusIdleEvery   = 60 * time.Second
	censusActiveFor   = 10 * time.Minute // a read keeps the fast cadence this long
	censusMaxAge      = 90 * time.Second // older than this (the refresher stalled), a read rebuilds
	censusBuildTime   = 25 * time.Second // one build's budget
	censusTick        = time.Second      // how often the refresher checks: builds start on time, so browsers can meet them
)

// censusCache holds the last census and runs at most one build per generation at a time.
type censusCache struct {
	build func(context.Context) ([]sensorRow, error)

	mu       sync.Mutex
	gen      uint64 // bumped by invalidate
	rows     []sensorRow
	rowsGen  uint64
	at       time.Time     // when the cached rows' build started (the data is as of then)
	took     time.Duration // how long that build took
	lastUse  time.Time
	building *censusBuild
}

type censusBuild struct {
	gen  uint64
	done chan struct{}
	rows []sensorRow
	at   time.Time
	took time.Duration
	err  error
}

// censusSnapshot is one census with the time it describes.
type censusSnapshot struct {
	Rows []sensorRow
	At   time.Time
	Took time.Duration
}

func newCensusCache(build func(context.Context) ([]sensorRow, error)) *censusCache {
	return &censusCache{build: build}
}

// get returns the census, from memory when it is of the current generation and recent enough,
// otherwise after the build in flight (or a new one) completes. A build is never tied to the
// caller's context: other readers share it.
func (c *censusCache) get(ctx context.Context) (censusSnapshot, error) {
	c.mu.Lock()
	c.lastUse = time.Now()
	if c.rows != nil && c.rowsGen == c.gen && time.Since(c.at) < censusMaxAge {
		snap := censusSnapshot{Rows: c.rows, At: c.at, Took: c.took}
		c.mu.Unlock()
		return snap, nil
	}
	b := c.startLocked()
	c.mu.Unlock()
	select {
	case <-b.done:
		if b.err != nil {
			return censusSnapshot{}, b.err
		}
		return censusSnapshot{Rows: b.rows, At: b.at, Took: b.took}, nil
	case <-ctx.Done():
		return censusSnapshot{}, ctx.Err()
	}
}

// startLocked returns the build of the current generation, starting one if none is in flight.
// The caller holds c.mu.
func (c *censusCache) startLocked() *censusBuild {
	if c.building != nil && c.building.gen == c.gen {
		return c.building
	}
	b := &censusBuild{gen: c.gen, done: make(chan struct{})}
	c.building = b
	go c.run(b)
	return b
}

func (c *censusCache) run(b *censusBuild) {
	ctx, cancel := context.WithTimeout(context.Background(), censusBuildTime)
	defer cancel()
	b.at = time.Now()
	b.rows, b.err = c.build(ctx)
	b.took = time.Since(b.at)
	c.mu.Lock()
	// A build that started before a newer one completed must not overwrite it.
	if b.err == nil && (c.rows == nil || b.gen >= c.rowsGen) {
		c.rows, c.rowsGen, c.at, c.took = b.rows, b.gen, b.at, b.took
	}
	if c.building == b {
		c.building = nil
	}
	c.mu.Unlock()
	close(b.done)
}

// invalidate marks the cached census as older than the change just made. It only bumps the
// generation: the next read (or the refresher's next tick) builds, so a burst of changes costs
// one build, not one each.
func (c *censusCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
}

// refreshDue reports whether the refresher should start a build now.
func (c *censusCache) refreshDue(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.building != nil {
		return false
	}
	if c.rows == nil || c.rowsGen != c.gen {
		return true
	}
	every := censusIdleEvery
	if now.Sub(c.lastUse) < censusActiveFor {
		every = censusActiveEvery
	}
	return now.Sub(c.at) >= every
}

// nextIn estimates how long until the next build is ready: the cadence after the last build started,
// plus as long as that build took. A browser asks again just after it, so its "updated ... ago"
// always restarts from a fresh build instead of landing at a random point between two.
func (c *censusCache) nextIn(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	every := censusIdleEvery
	if now.Sub(c.lastUse) < censusActiveFor {
		every = censusActiveEvery
	}
	d := c.at.Add(every + c.took).Sub(now)
	if d < time.Second {
		d = time.Second // due or building now
	}
	return d
}

// refresh starts a build without waiting for it.
func (c *censusCache) refresh() {
	c.mu.Lock()
	c.startLocked()
	c.mu.Unlock()
}

// startCensusRefresh keeps the census warm for as long as the server runs (skipped until a Zabbix
// token is configured).
func (s *Server) startCensusRefresh(ctx context.Context) {
	go func() {
		t := time.NewTicker(censusTick)
		defer t.Stop()
		for {
			if s.zbx.Authenticated() && s.census.refreshDue(time.Now()) {
				s.census.refresh()
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// censusMachinePaths are the routes probes and enrollment call on their own schedule; they don't
// change what the census shows, and they arrive every minute from every probe.
var censusMachinePaths = []string{"/api/probes/checkin", "/api/probes/break-glass", "/api/probes/os-status", "/api/probes/scan-results", "/api/enroll"}

// censusInvalidator marks the census stale after any change a signed-in user makes (and after an
// acknowledgement through an alert's signed link), once the handler has finished, so a build that
// starts afterwards sees the change.
func (s *Server) censusInvalidator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		for _, p := range censusMachinePaths {
			if r.URL.Path == p {
				return
			}
		}
		if _, signedIn := auth.UserFrom(r.Context()); signedIn || r.URL.Path == "/api/alert/ack" {
			s.census.invalidate()
		}
	})
}

// censusStates are the sensor states the census reports.
var censusStates = []string{"ok", "warning", "error", "acked", "paused", "hidden"}

// handleCensus answers the status pills and the sensor lists in one call:
//
//	GET /api/census?rows=error,warning,acked
//
// returns every state's count and the rows of the requested states only (the OK list alone is most
// of the census, and only the OK drill-down needs it). built_at is when the data was read, age_ms
// how old it is now (so a browser with a skewed clock still shows the right "updated ... ago"), and
// next_ms when the next build should be ready (the app asks again just after it).
func (s *Server) handleCensus(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	want := map[string]bool{}
	for _, st := range strings.Split(r.URL.Query().Get("rows"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			want[st] = true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), censusBuildTime)
	defer cancel()
	snap, err := s.census.get(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	counts := make(map[string]int, len(censusStates))
	for _, st := range censusStates {
		counts[st] = 0
	}
	rows := []sensorRow{}
	for _, sr := range snap.Rows {
		counts[sr.State]++
		if want[sr.State] {
			rows = append(rows, sr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"counts":   counts,
		"rows":     rows,
		"built_at": snap.At.Unix(),
		"age_ms":   time.Since(snap.At).Milliseconds(),
		"next_ms":  s.census.nextIn(time.Now()).Milliseconds(),
		"build_ms": snap.Took.Milliseconds(),
	})
}
