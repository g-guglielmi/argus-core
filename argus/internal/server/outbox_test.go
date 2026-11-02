// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// hookServer is a webhook that is down (502) until told to answer, keeping the bodies it got.
type hookServer struct {
	*httptest.Server
	mu      sync.Mutex
	answers bool
	bodies  []string
}

func newHookServer(t *testing.T) *hookServer {
	h := &hookServer{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.answers {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		h.bodies = append(h.bodies, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) up(answers bool) {
	h.mu.Lock()
	h.answers = answers
	h.mu.Unlock()
}

func (h *hookServer) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

// outboxEnv is a notifier run whose one channel posts to a webhook that is down for now, with the
// outbox retrying on every pass.
func outboxEnv(t *testing.T, problems []tickProblem) (*tickEnv, *hookServer) {
	t.Helper()
	hook := newHookServer(t)
	e := newTickEnv(t, "1", "0", problems)
	ctx := t.Context()
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
	return e, hook
}

var adguardDown = []tickProblem{{event: "1", name: "AdGuard is not running", itemID: "101", key: "adguard.running", sev: "4"}}

// A send that fails (the core without internet, the service down) is not lost: it waits in the
// outbox and goes out when the channel answers again while the problem is still open, the timeline
// says so, and the RESOLVED that follows takes the same path.
func TestFailedSendsGoOutWhenTheChannelAnswersAgain(t *testing.T) {
	e, hook := outboxEnv(t, adguardDown)
	ctx := t.Context()

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
	if len(hook.got()) != 0 {
		t.Fatalf("nothing should have reached the webhook yet, got %d", len(hook.got()))
	}

	hook.up(true)
	e.tick() // the webhook answers and the problem is still open: the alert goes out, once
	if got := hook.got(); len(got) != 1 || !strings.Contains(got[0], "AdGuard is not running") {
		t.Fatalf("want the alert delivered once, got %q", got)
	}
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 0 {
		t.Fatalf("want the outbox empty, got %+v", waiting)
	}
	if tl := e.timeline("1"); !hasEntry(tl, "alerted: sent ") {
		t.Fatalf("want the timeline to say the alert went out late, got %v", tl)
	}

	// The problem closes while the webhook is down again: the channel heard the alert, so its
	// RESOLVED waits too, then goes out.
	hook.up(false)
	e.problems = nil
	e.tick()
	if tl := e.timeline("1"); !strings.Contains(strings.Join(tl, ";"), "(0 channels told, 1 still trying)") {
		t.Fatalf("want the RESOLVED counted as still trying, got %v", tl)
	}
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 1 || waiting[0].Kind != "recovery" {
		t.Fatalf("want the RESOLVED waiting in the outbox, got %+v", waiting)
	}
	hook.up(true)
	e.tick()
	if got := hook.got(); len(got) != 2 {
		t.Fatalf("want the RESOLVED delivered, got %d sends", len(got))
	}
	if tl := e.timeline("1"); !hasEntry(tl, "resolved: RESOLVED reached ops (webhook) ") {
		t.Fatalf("want the timeline to say the RESOLVED reached the channel late, got %v", tl)
	}
}

// An incident that opens and closes while the channel cannot be reached sends neither its alert
// nor a RESOLVED once the channel is back: it is one line of the channel's recap.
func TestAnIncidentThatCameAndWentIsRecapped(t *testing.T) {
	e, hook := outboxEnv(t, adguardDown)
	ctx := t.Context()
	e.tick()
	e.tick() // the alert waits
	e.problems = nil
	e.tick() // it closes: the alert never got out, nothing to resolve
	if tl := e.timeline("1"); !strings.Contains(strings.Join(tl, ";"), "(0 channels told, 1 never heard of it (told in a recap))") {
		t.Fatalf("want the RESOLVED counted as never heard, got %v", tl)
	}
	waiting, _ := e.st.OutboxWaiting(ctx)
	if len(waiting) != 1 || waiting[0].Kind != "info" || waiting[0].EventID != recapEvent {
		t.Fatalf("want only the recap waiting, got %+v", waiting)
	}
	hook.up(true)
	e.tick()
	got := hook.got()
	if len(got) != 1 || !strings.Contains(got[0], "1 incident came and went while this channel could not be reached") || !strings.Contains(got[0], "adguard1: AdGuard is not running, ") {
		t.Fatalf("want the recap as the one send, got %q", got)
	}
	if waiting, _ = e.st.OutboxWaiting(ctx); len(waiting) != 0 {
		t.Fatalf("want the outbox empty after the recap, got %+v", waiting)
	}
}

// The drain sends an incident's queued sends in order (a RESOLVED waits for the alert before it),
// drops what is no longer true (a reminder, an acknowledged notice of a closed problem) and an alert
// whose problem closed goes into the recap, whose RESOLVED behind it is dropped with it.
func TestTheOutboxKeepsOrderAndSendsOnlyWhatIsStillTrue(t *testing.T) {
	e := newTickEnv(t, "1", "0", nil)
	ctx := t.Context()
	var sent []string
	dest := notifyDest{key: "g:9", label: "ops (Discord)", send: func(_ context.Context, ev notify.Event) error { sent = append(sent, ev.Kind+":"+ev.Name); return nil }}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()
	queue := func(event, kind, name string, when time.Time, nextAt int64) {
		raw, _ := json.Marshal(notify.Event{Kind: kind, Name: name, Host: "fw1", When: when})
		if err := e.st.QueueOutbox(ctx, store.Outbox{EventID: event, DestKey: dest.key, Kind: kind, Label: dest.label, Event: raw, QueuedAt: now.Unix() - 60, NextAt: nextAt}); err != nil {
			t.Fatal(err)
		}
	}
	queue("5", "problem", "Ping lost", now.Add(-time.Hour), now.Unix()+300) // not due yet
	queue("5", "recovery", "Ping answers again", now, 0)                    // due, but behind the alert
	queue("6", "reminder", "Disk full", now, 0)
	queue("7", "ack", "CPU high", now, 0)
	queue("8", "problem", "Link down", now.Add(-30*time.Minute), 0)
	queue("8", "recovery", "Link up again", now.Add(-10*time.Minute), 0)
	open := map[string]zabbix.Problem{"5": {}, "6": {}}

	pending, recapped := drainOutbox(ctx, e.st, []notifyDest{dest}, open, logger, now)
	if len(sent) != 0 {
		t.Fatalf("nothing is due that is still true and not behind another send, got %v", sent)
	}
	if !pending[pendingKey("5", dest.key)] || pending[pendingKey("6", dest.key)] || pending[pendingKey("7", dest.key)] || pending[pendingKey("8", dest.key)] {
		t.Fatalf("want only incident 5 still pending (6 and 7 dropped, 8 recapped), got %v", pending)
	}
	if len(recapped) != 1 || !recapped[pendingKey("8", dest.key)] {
		t.Fatalf("want incident 8 reported as recapped, got %v", recapped)
	}
	waiting, _ := e.st.OutboxWaiting(ctx)
	kinds := []string{}
	for _, w := range waiting {
		kinds = append(kinds, w.EventID+":"+w.Kind)
	}
	if strings.Join(kinds, " ") != "5:problem 5:recovery :info" {
		t.Fatalf("want the alert, its RESOLVED and the recap waiting, got %v", kinds)
	}
	if tl := e.timeline("8"); !hasEntry(tl, "send_failed: it was over before the channel could be reached") {
		t.Fatalf("want incident 8's timeline to say it went into the recap, got %v", tl)
	}

	// The alert's wait is over: it goes out, then its RESOLVED, then the recap.
	if err := e.st.OutboxRetry(ctx, waiting[0].ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	pending, _ = drainOutbox(ctx, e.st, []notifyDest{dest}, open, logger, now)
	if strings.Join(sent, " ") != "problem:Ping lost recovery:Ping answers again info:1 incident came and went while this channel could not be reached" || len(pending) != 0 {
		t.Fatalf("want the alert, its RESOLVED and the recap in that order, got %v (pending %v)", sent, pending)
	}
	if tl := e.timeline("5"); !hasEntry(tl, "alerted: sent ") || !hasEntry(tl, "resolved: RESOLVED reached ops (Discord) ") {
		t.Fatalf("want incident 5's timeline to show both late sends, got %v", tl)
	}
}

// A recap grows one line per incident and counts them in its title.
func TestARecapGrowsByOneLinePerIncident(t *testing.T) {
	e := newTickEnv(t, "1", "0", nil)
	ctx := t.Context()
	dest := notifyDest{key: "g:9", label: "ops (Discord)"}
	loc := time.FixedZone("CET", 3600)
	began := time.Date(2026, 11, 2, 1, 33, 0, 0, loc)
	recapDropped(ctx, e.st, dest, "Firewall-RN", "UniFi controller does not answer", began, began.Add(5*time.Minute))
	recapDropped(ctx, e.st, dest, "sw1", "Ping lost", began.Add(20*time.Hour), began.Add(26*time.Hour))
	rows, _ := e.st.OutboxPair(ctx, recapEvent, dest.key)
	if len(rows) != 1 {
		t.Fatalf("want one recap row, got %d", len(rows))
	}
	var ev notify.Event
	_ = json.Unmarshal(rows[0].Event, &ev)
	if ev.Kind != "info" || ev.Name != "2 incidents came and went while this channel could not be reached" {
		t.Fatalf("want the recap titled by its count, got %+v", ev)
	}
	if ev.Detail != "Firewall-RN: UniFi controller does not answer, Nov 2 01:33 to 01:38\nsw1: Ping lost, Nov 2 21:33 to Nov 3 03:33" {
		t.Fatalf("want one line per incident with its times, got %q", ev.Detail)
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
