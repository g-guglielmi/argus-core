// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

// A send that fails for a reason other than a rate limit (the core without internet, the service
// down, its DNS gone) used to be lost: the alert was recorded as delivered and never tried again,
// and a recovery had no second chance at all. It now waits in the outbox (notify_outbox) and goes
// again with growing gaps, for up to a day.
//
// The outbox delivers the present, not the past: once the destination answers again, an alert goes
// out only if its problem is still open, a RESOLVED only for an alert the destination heard, and
// an incident that came and went meanwhile is folded into one recap per destination ("3 incidents
// came and went while this channel could not be reached", one line each). Reminders waiting are
// dropped (the next comes on schedule). Sends to one destination for one incident keep their
// order: a later one never overtakes an earlier one still waiting.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

var (
	outboxFirstRetry = 30 * time.Second // the gap before the first retry; it doubles each failure
	outboxMaxGap     = 10 * time.Minute // the longest gap between retries
	outboxKeep       = 24 * time.Hour   // a send still failing after this is given up on
)

// errBehindEarlier is why a send was queued without a try: an earlier send to that destination for
// the incident is still being retried, and this one must not overtake it.
var errBehindEarlier = errors.New("waits behind an earlier send to it that is still being retried")

// recapEvent is the outbox's incident id for a destination's recap, which has no incident of its own.
const recapEvent = ""

// outboxPending is the incident+destination pairs with a send waiting in the outbox.
type outboxPending map[string]bool

func pendingKey(eventID, destKey string) string { return eventID + "|" + destKey }

// deliver sends ev to d for the incident, or queues it in the outbox: behind a send to d still
// waiting for this incident, or after the send fails. nil means it went out now; otherwise the
// error says why it waits, and the outbox retries it.
func deliver(ctx context.Context, st *store.Store, pending outboxPending, d notifyDest, eventID string, ev notify.Event) error {
	k := pendingKey(eventID, d.key)
	err := errBehindEarlier
	if !pending[k] {
		if err = d.send(ctx, ev); err == nil {
			return nil
		}
	}
	raw, jerr := json.Marshal(ev)
	if jerr != nil {
		return err
	}
	now := time.Now().Unix()
	row := store.Outbox{EventID: eventID, DestKey: d.key, Kind: ev.Kind, Label: d.label, Event: raw, QueuedAt: now, NextAt: now + int64(outboxFirstRetry/time.Second)}
	if qerr := st.QueueOutbox(ctx, row); qerr != nil {
		return err
	}
	pending[k] = true
	return err
}

// drainOutbox walks the queued sends oldest first, sends what is due and still true (open is the
// problems open now), drops what is not, and returns the incident+destination pairs still waiting
// and those whose alert it just folded into a recap (their RESOLVED, if one follows this pass, is
// nothing to send).
// A pair's later sends wait while an earlier one is not due, fails or is dropped; a destination
// that fails keeps the rest of its queue for a later pass. A send is dropped once its destination
// is gone (the channel deleted or turned off) or after outboxKeep.
func drainOutbox(ctx context.Context, st *store.Store, dests []notifyDest, open map[string]zabbix.Problem, logger *slog.Logger, now time.Time) (pending, recapped outboxPending) {
	recapped = outboxPending{}
	byKey := map[string]notifyDest{}
	for _, d := range dests {
		byKey[d.key] = d
	}
	rows, err := st.OutboxAll(ctx)
	if err != nil {
		logger.Warn("notifier: load outbox", "err", err)
	}
	failed := map[string]bool{}  // destinations that failed this pass
	blocked := map[string]bool{} // pairs whose earlier send did not go out this pass
	for _, o := range rows {
		pair := pendingKey(o.EventID, o.DestKey)
		d, ok := byKey[o.DestKey]
		switch {
		case !ok:
			_ = st.DeleteOutbox(ctx, o.ID)
			tlog(ctx, st, o.EventID, "send_failed", "", o.Label, "dropped: the channel is gone or off", 0)
			continue
		case failed[o.DestKey] || blocked[pair]:
			continue
		case o.NextAt > now.Unix():
			blocked[pair] = true
			continue
		case now.Unix()-o.QueuedAt > int64(outboxKeep/time.Second):
			_ = st.DeleteOutboxPair(ctx, o.EventID, o.DestKey)
			blocked[pair] = true
			tlog(ctx, st, o.EventID, "send_failed", "", o.Label, fmt.Sprintf("given up after a day and %d more tries", o.Attempts), 0)
			continue
		}
		var ev notify.Event
		if err := json.Unmarshal(o.Event, &ev); err != nil {
			_ = st.DeleteOutbox(ctx, o.ID)
			continue
		}
		// Only what is still true goes out.
		_, isOpen := open[o.EventID]
		switch {
		case ev.Kind == "reminder", ev.Kind == "ack" && !isOpen:
			_ = st.DeleteOutbox(ctx, o.ID)
			continue
		case ev.Kind == "problem" && !isOpen:
			// It came and went while the destination could not be reached: the recap tells of it,
			// and its RESOLVED waiting behind it goes with it.
			ended := now
			if pairRows, _ := st.OutboxPair(ctx, o.EventID, o.DestKey); len(pairRows) > 0 {
				for _, r := range pairRows {
					var rev notify.Event
					if r.Kind == "recovery" && json.Unmarshal(r.Event, &rev) == nil && !rev.When.IsZero() {
						ended = rev.When
					}
				}
			}
			recapDropped(ctx, st, d, ev.Host, ev.Name, ev.When, ended)
			_ = st.DeleteOutboxPair(ctx, o.EventID, o.DestKey)
			blocked[pair], recapped[pair] = true, true
			tlog(ctx, st, o.EventID, "send_failed", "", o.Label, "it was over before the channel could be reached: dropped, told in a recap", 0)
			continue
		}
		if err := d.send(ctx, ev); err != nil {
			failed[o.DestKey] = true
			attempts := o.Attempts + 1
			gap := min(outboxFirstRetry<<uint(min(attempts, 6)), outboxMaxGap)
			_ = st.OutboxRetry(ctx, o.ID, attempts, now.Unix()+int64(gap/time.Second))
			logger.Info("notifier: send still failing, tries again", "event", o.EventID, "to", o.Label, "kind", o.Kind, "attempts", attempts, "in", gap, "err", err)
			continue
		}
		_ = st.DeleteOutbox(ctx, o.ID)
		late := notify.FormatDuration(now.Unix() - o.QueuedAt)
		switch ev.Kind {
		case "ack":
			tlog(ctx, st, o.EventID, "ack_told", "", o.Label, o.Label+", "+late+" late", 0)
		case "recovery":
			tlog(ctx, st, o.EventID, "resolved", "", o.Label, "RESOLVED reached "+o.Label+" "+late+" late", 0)
		case "problem":
			tlog(ctx, st, o.EventID, "alerted", "", o.Label, "sent "+late+" late", 0)
		}
		logger.Info("notifier: queued send went out", "event", o.EventID, "to", o.Label, "kind", o.Kind, "late", late)
	}
	pending = outboxPending{}
	waiting, err := st.OutboxWaiting(ctx)
	if err != nil {
		logger.Warn("notifier: load outbox", "err", err)
	}
	for _, o := range waiting {
		pending[pendingKey(o.EventID, o.DestKey)] = true
	}
	return pending, recapped
}

// recapDropped adds an incident that came and went while the destination could not be reached to
// the recap waiting for it, one line per incident; the recap goes out, as one notice, once the
// destination answers again.
func recapDropped(ctx context.Context, st *store.Store, d notifyDest, host, name string, began, ended time.Time) {
	ev := notify.Event{Kind: "info", State: "ok"}
	var row *store.Outbox
	if rows, _ := st.OutboxPair(ctx, recapEvent, d.key); len(rows) > 0 {
		row = &rows[0]
		_ = json.Unmarshal(row.Event, &ev)
	}
	ended = ended.In(began.Location())
	to := ended.Format("15:04")
	if ended.YearDay() != began.YearDay() || ended.Year() != began.Year() {
		to = ended.Format("Jan 2 15:04")
	}
	ev.Detail = strings.TrimSpace(ev.Detail + "\n" + fmt.Sprintf("%s: %s, %s to %s", host, name, began.Format("Jan 2 15:04"), to))
	n := strings.Count(ev.Detail, "\n") + 1
	ev.Name = fmt.Sprintf("%d incidents came and went while this channel could not be reached", n)
	if n == 1 {
		ev.Name = "1 incident came and went while this channel could not be reached"
	}
	ev.When = ended
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if row != nil {
		_ = st.UpdateOutboxEvent(ctx, row.ID, raw)
		return
	}
	now := time.Now().Unix()
	_ = st.QueueOutbox(ctx, store.Outbox{EventID: recapEvent, DestKey: d.key, Kind: "info", Label: d.label, Event: raw, QueuedAt: now, NextAt: now})
}

// toldCount words how a RESOLVED or acknowledged notice fared: "2 channels told, 1 still trying,
// 1 never heard of it (told in a recap)".
func toldCount(told, trying, dropped int) string {
	out := fmt.Sprintf("%d channels told", told)
	if trying > 0 {
		out += fmt.Sprintf(", %d still trying", trying)
	}
	if dropped > 0 {
		out += fmt.Sprintf(", %d never heard of it (told in a recap)", dropped)
	}
	return out
}

// retryNote is the timeline's word on a send that waits in the outbox.
func retryNote(err error) string {
	if errors.Is(err, errBehindEarlier) {
		return err.Error()
	}
	return err.Error() + " (tries again)"
}
