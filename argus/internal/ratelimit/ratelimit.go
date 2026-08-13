// Package ratelimit provides a small in-memory sliding-window failure counter, used to throttle
// repeated failed logins (brute-force protection). It is process-local and lock-guarded.
package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu     sync.Mutex
	fails  map[string][]int64 // key -> unix timestamps of recent failures
	max    int
	window int64 // seconds
}

// New returns a limiter that blocks a key once it accumulates max failures within window.
func New(max int, window time.Duration) *Limiter {
	if max < 1 {
		max = 1
	}
	return &Limiter{fails: map[string][]int64{}, max: max, window: int64(window.Seconds())}
}

// Blocked reports whether the key currently has >= max failures inside the window, and if so how
// long until the oldest counted failure ages out (the retry-after hint).
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	f := prune(l.fails[key], now-l.window)
	if len(f) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = f
	}
	if len(f) >= l.max {
		retry := f[0] + l.window - now
		if retry < 0 {
			retry = 0
		}
		return true, time.Duration(retry) * time.Second
	}
	return false, 0
}

// Fail records one failure against the key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	l.fails[key] = append(prune(l.fails[key], now-l.window), now)
	if len(l.fails) > 8192 { // guard against unbounded growth from spraying
		l.sweep(now)
	}
}

// Reset clears a key's failures (called after a successful authentication).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *Limiter) sweep(now int64) {
	cutoff := now - l.window
	for k, f := range l.fails {
		if pf := prune(f, cutoff); len(pf) == 0 {
			delete(l.fails, k)
		} else {
			l.fails[k] = pf
		}
	}
}

// prune drops timestamps older than cutoff (the slice is sorted ascending by construction).
func prune(f []int64, cutoff int64) []int64 {
	i := 0
	for i < len(f) && f[i] < cutoff {
		i++
	}
	if i == 0 {
		return f
	}
	return f[i:]
}
