// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"argus/internal/notify"
)

// Sends to one destination are spaced, and a service's "too many" is waited out once and sent again.
func TestPacedSendsWaitOutARateLimit(t *testing.T) {
	pacing.next = nil
	calls := 0
	send := func(ctx context.Context, ev notify.Event) error {
		calls++
		if calls == 1 {
			return &notify.RateLimited{After: 20 * time.Millisecond}
		}
		return nil
	}
	p := paced("g:1", "telegram", send)
	t0 := time.Now()
	if err := p(context.Background(), notify.Event{}); err != nil || calls != 2 {
		t.Fatalf("first send: err %v, calls %d", err, calls)
	}
	if time.Since(t0) < 20*time.Millisecond {
		t.Fatal("didn't wait the pause asked for")
	}
	// The destination's next send waits the service's gap (3 s for Telegram), not more.
	if next := pacing.next["g:1"]; next.Before(time.Now().Add(2*time.Second)) || next.After(time.Now().Add(4*time.Second)) {
		t.Fatalf("next send at %s", time.Until(next))
	}
	// A pause longer than a tick can hold is left for the next tick: the error goes back as it is.
	pacing.next = nil
	long := func(ctx context.Context, ev notify.Event) error { return &notify.RateLimited{After: 10 * time.Minute} }
	if err := paced("g:2", "discord", long)(context.Background(), notify.Event{}); err == nil {
		t.Fatal("a long pause was swallowed")
	} else if _, ok := notify.AsRateLimited(err); !ok {
		t.Fatalf("not the rate limit: %v", err)
	}
	if next := pacing.next["g:2"]; next.Before(time.Now().Add(9 * time.Minute)) {
		t.Fatal("the destination isn't paused for the wait asked")
	}
	// Other failures pass through once, unpaced.
	pacing.next = nil
	boom := errors.New("HTTP 502")
	if err := paced("g:3", "slack", func(context.Context, notify.Event) error { return boom })(context.Background(), notify.Event{}); err != boom {
		t.Fatalf("a plain failure changed: %v", err)
	}
	// A context that ends while waiting ends the send.
	pacing.next = map[string]time.Time{"g:4": time.Now().Add(time.Hour)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := paced("g:4", "teams", send)(ctx, notify.Event{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waited past the context: %v", err)
	}
	// Email is not paced at all.
	pacing.next = nil
	calls = 0
	if err := paced("g:5", "email", send)(context.Background(), notify.Event{}); err == nil || calls != 1 {
		t.Fatalf("email paced: err %v, calls %d", err, calls)
	}
	if sendGap("telegram") != 3*time.Second || sendGap("discord") != 2*time.Second || sendGap("slack") != time.Second || sendGap("email") != 0 {
		t.Fatal("gaps")
	}
}
