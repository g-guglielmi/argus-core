// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// A fake census source: every build returns rows labelled with the build number, optionally
// blocking until released so a test can hold a build in flight.
type fakeCensus struct {
	n    atomic.Int32
	gate chan struct{} // nil = don't block
}

func (f *fakeCensus) build(ctx context.Context) ([]sensorRow, error) {
	n := f.n.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []sensorRow{{ItemID: string(rune('0' + n)), State: "ok"}}, nil
}

func TestCensusCacheServesFromMemory(t *testing.T) {
	f := &fakeCensus{}
	c := newCensusCache(f.build)
	ctx := context.Background()
	a, err := c.get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.n.Load() != 1 || a.Rows[0].ItemID != b.Rows[0].ItemID {
		t.Fatalf("second read should come from memory: builds=%d", f.n.Load())
	}
}

// Concurrent readers of a cold cache share one build.
func TestCensusCacheSingleFlight(t *testing.T) {
	f := &fakeCensus{gate: make(chan struct{})}
	c := newCensusCache(f.build)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.get(context.Background()) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	if n := f.n.Load(); n != 1 {
		t.Fatalf("builds = %d, want 1", n)
	}
}

// After invalidate, a read waits for a build of the new generation, and a build that started
// before the change can't overwrite it.
func TestCensusCacheInvalidate(t *testing.T) {
	f := &fakeCensus{}
	c := newCensusCache(f.build)
	ctx := context.Background()
	if _, err := c.get(ctx); err != nil {
		t.Fatal(err)
	}
	// Hold a build in flight, then change something.
	f.gate = make(chan struct{})
	c.mu.Lock()
	c.gen++ // as if the refresher saw a stale generation
	old := c.startLocked()
	c.mu.Unlock()
	for f.n.Load() < 2 { // the pre-change build has started (and numbered itself 2)
		time.Sleep(time.Millisecond)
	}
	c.invalidate()
	done := make(chan censusSnapshot)
	go func() { s, _ := c.get(ctx); done <- s }()
	time.Sleep(50 * time.Millisecond)
	close(f.gate) // both the old and the new build finish
	<-old.done
	snap := <-done
	if f.n.Load() != 3 {
		t.Fatalf("builds = %d, want 3 (initial, pre-change, post-change)", f.n.Load())
	}
	if snap.Rows[0].ItemID != "3" {
		t.Fatalf("read after invalidate got build %q, want the post-change build", snap.Rows[0].ItemID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rowsGen != c.gen || c.rows[0].ItemID != "3" {
		t.Fatalf("cache holds build %q gen %d (current %d), want the post-change build", c.rows[0].ItemID, c.rowsGen, c.gen)
	}
}

func TestCensusRefreshDue(t *testing.T) {
	c := newCensusCache((&fakeCensus{}).build)
	now := time.Now()
	if !c.refreshDue(now) {
		t.Fatal("an empty cache is due")
	}
	c.rows, c.at, c.lastUse = []sensorRow{}, now, now
	if c.refreshDue(now.Add(censusActiveEvery - time.Second)) {
		t.Fatal("fresh and in use: not due yet")
	}
	if !c.refreshDue(now.Add(censusActiveEvery)) {
		t.Fatal("in use: due after the active cadence")
	}
	c.lastUse = now.Add(-censusActiveFor - time.Minute)
	if c.refreshDue(now.Add(censusActiveEvery)) {
		t.Fatal("idle: the active cadence doesn't apply")
	}
	if !c.refreshDue(now.Add(censusIdleEvery)) {
		t.Fatal("idle: due after the idle cadence")
	}
	c.invalidate()
	if !c.refreshDue(now) {
		t.Fatal("a stale generation is due at once")
	}
}

// The invalidator fires after a signed-in user's change and after a signed-link ack, never for a
// read or a probe's own traffic.
func TestCensusInvalidator(t *testing.T) {
	s := &Server{census: newCensusCache((&fakeCensus{}).build)}
	h := s.censusInvalidator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	user := func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), &store.User{ID: 1, Role: "admin"}))
	}
	cases := []struct {
		req  *http.Request
		want bool
	}{
		{user(httptest.NewRequest("POST", "/api/events/1/ack", nil)), true},
		{user(httptest.NewRequest("DELETE", "/api/items/5/pause", nil)), true},
		{httptest.NewRequest("POST", "/api/alert/ack", nil), true},
		{user(httptest.NewRequest("GET", "/api/census", nil)), false},
		{httptest.NewRequest("POST", "/api/probes/checkin", nil), false},
		{httptest.NewRequest("POST", "/api/login", nil), false},
	}
	for _, tc := range cases {
		before := s.census.gen
		h.ServeHTTP(httptest.NewRecorder(), tc.req)
		if got := s.census.gen != before; got != tc.want {
			t.Errorf("%s %s: invalidated = %v, want %v", tc.req.Method, tc.req.URL.Path, got, tc.want)
		}
	}
}

// A browser is told to come back just after the next build: the cadence after the last build
// started plus that build's duration, never less than a second.
func TestCensusNextIn(t *testing.T) {
	c := newCensusCache((&fakeCensus{}).build)
	now := time.Now()
	c.at, c.took, c.lastUse = now.Add(-5*time.Second), 2*time.Second, now
	if got := c.nextIn(now); got != censusActiveEvery-5*time.Second+2*time.Second {
		t.Fatalf("active: next in %v", got)
	}
	c.lastUse = now.Add(-censusActiveFor - time.Minute)
	if got := c.nextIn(now); got != censusIdleEvery-5*time.Second+2*time.Second {
		t.Fatalf("idle: next in %v", got)
	}
	c.at = now.Add(-2 * censusIdleEvery)
	if got := c.nextIn(now); got != time.Second {
		t.Fatalf("overdue: next in %v, want 1s", got)
	}
}
