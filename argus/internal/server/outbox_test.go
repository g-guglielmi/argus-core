// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"argus/internal/notify"
)

// A send that fails (the core without internet, the service down) is not lost: it waits in the
// outbox and goes out when the channel answers again, the timeline says so, and the RESOLVED that
// follows takes the same path.
func TestFailedSendsGoOutWhenTheChannelAnswersAgain(t *testing.T) {
	var answers atomic.Bool
	var got atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !answers.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		got.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hook.Close)
	e := newTickEnv(t, "1", "0", []tickProblem{{event: "1", name: "AdGuard is not running", itemID: "101", key: "adguard.running", sev: "4"}})
	ctx := t.Context()
	// The channel posts to a webhook that is down for now.
	chans, err := e.st.EnabledNotifyChannels(ctx)
	if err != nil || len(chans) != 1 {
		t.Fatalf("want the one channel, got %d (%v)", len(chans), err)
	}
	ch := chans[0]
	ch.Type, ch.Config = "webhook", map[string]string{"webhook_url": hook.URL}
	if err := e.st.UpdateNotifyChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	outboxFirstRetry = 0 // every queued send is due on the next pass
	t.Cleanup(func() { outboxFirstRetry = 30 * time.Second })

	e.tick() // seen
	e.tick() // alerts: the send fails and waits
	if sent := e.sent(); len(sent) != 1 {
		t.Fatalf("the channel counts as alerted while its send waits, got %v", sent)
	}
	waiting, _ := e.st.OutboxWaiting(ctx)
	if len(waiting) != 1 || waiting[0].Kind != "problem" || waiting[0].Attempts != 0 {
		t.Fatalf("want the alert waiting in the outbox, got %+v", waiting)
	}
	if tl := e.timeline("1"); !hasEntry(tl, "send_failed: ") || !strings.Contains(strings.Join(tl, ";"), "(tries again)") {
		t.Fatalf("want the timeline to say the send is tried again, got %v", tl)
	}

	e.tick() // still down: the retry fails and backs off, nothing is sent twice
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 1 || waiting[0].Attempts != 1 {
		t.Fatalf("want one retry counted, got %+v", waiting)
	}
	if got.Load() != 0 {
		t.Fatalf("nothing should have reached the webhook yet, got %d", got.Load())
	}

	answers.Store(true)
	e.tick() // the webhook answers: the alert goes out, once
	if got.Load() != 1 {
		t.Fatalf("want the alert delivered once, got %d", got.Load())
	}
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 0 {
		t.Fatalf("want the outbox empty, got %+v", waiting)
	}
	if tl := e.timeline("1"); !hasEntry(tl, "alerted: sent ") {
		t.Fatalf("want the timeline to say the alert went out late, got %v", tl)
	}

	// The problem closes while the webhook is down again: the RESOLVED waits too, then goes out.
	answers.Store(false)
	e.problems = nil
	e.tick()
	if tl := e.timeline("1"); !strings.Contains(strings.Join(tl, ";"), "(0 channels told, 1 still trying)") {
		t.Fatalf("want the RESOLVED counted as still trying, got %v", tl)
	}
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 1 || waiting[0].Kind != "recovery" {
		t.Fatalf("want the RESOLVED waiting in the outbox, got %+v", waiting)
	}
	answers.Store(true)
	e.tick()
	if got.Load() != 2 {
		t.Fatalf("want the RESOLVED delivered, got %d sends", got.Load())
	}
	if tl := e.timeline("1"); !hasEntry(tl, "resolved: RESOLVED reached ops (webhook) ") {
		t.Fatalf("want the timeline to say the RESOLVED reached the channel late, got %v", tl)
	}
}

// A send to a destination with an earlier send still waiting for the same incident queues behind
// it instead of overtaking it; another incident's send to that destination is not held up.
func TestLaterSendsQueueBehindAWaitingOne(t *testing.T) {
	e := newTickEnv(t, "1", "0", nil)
	ctx := t.Context()
	sends := 0
	dest := notifyDest{key: "g:1", label: "ops (Discord)", send: func(context.Context, notify.Event) error { sends++; return nil }}
	pending := outboxPending{pendingKey("7", dest.key): true}
	if err := deliver(ctx, e.st, pending, dest, "7", notify.Event{Kind: "recovery"}); !errors.Is(err, errBehindEarlier) || sends != 0 {
		t.Fatalf("want the RESOLVED queued behind the waiting alert without a try, got err=%v sends=%d", err, sends)
	}
	if waiting, _ := e.st.OutboxWaiting(ctx); len(waiting) != 1 || waiting[0].Kind != "recovery" || waiting[0].EventID != "7" {
		t.Fatalf("want the RESOLVED in the outbox, got %+v", waiting)
	}
	if err := deliver(ctx, e.st, pending, dest, "8", notify.Event{Kind: "problem"}); err != nil || sends != 1 {
		t.Fatalf("want another incident's alert sent at once, got err=%v sends=%d", err, sends)
	}
	if retryNote(errBehindEarlier) != errBehindEarlier.Error() || retryNote(errors.New("502 Bad Gateway")) != "502 Bad Gateway (tries again)" {
		t.Fatal("the timeline note should say a failed send is tried again, and a queued one why it waits")
	}
}

// hasEntry reports whether a timeline has an entry starting with the prefix.
func hasEntry(tl []string, prefix string) bool {
	for _, x := range tl {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}
