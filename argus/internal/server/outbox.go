// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

// A send that fails for a reason other than a rate limit (the core without internet, the service
// down, its DNS gone) used to be lost: the alert was recorded as delivered and never tried again,
// and a recovery had no second chance at all. It now waits in the outbox (notify_outbox) and goes
// again with growing gaps, for up to a day. Sends to one destination for one incident keep their
// order: a reminder, the acknowledged notice or the RESOLVED queue behind an alert still waiting,
// so when the link is back the channel reads the incident in the order it happened.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
)

var (
	outboxFirstRetry = 30 * time.Second // the gap before the first retry; it doubles each failure
	outboxMaxGap     = 10 * time.Minute // the longest gap between retries
	outboxKeep       = 24 * time.Hour   // a send still failing after this is given up on
)

// errBehindEarlier is why a send was queued without a try: an earlier send to that destination for
// the incident is still being retried, and this one must not overtake it.
var errBehindEarlier = errors.New("waits behind an earlier send to it that is still being retried")

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

// drainOutbox retries the queued sends whose wait is over, oldest first, and returns the pairs
// still waiting. A destination that fails again keeps the rest of its queue for a later pass, so
// its sends stay in order. A send is dropped once its destination is gone (the channel deleted or
// turned off) or after outboxKeep.
func drainOutbox(ctx context.Context, st *store.Store, dests []notifyDest, logger *slog.Logger, now time.Time) outboxPending {
	byKey := map[string]notifyDest{}
	for _, d := range dests {
		byKey[d.key] = d
	}
	rows, err := st.OutboxDue(ctx, now.Unix())
	if err != nil {
		logger.Warn("notifier: load outbox", "err", err)
	}
	failed := map[string]bool{}
	for _, o := range rows {
		d, ok := byKey[o.DestKey]
		switch {
		case !ok:
			_ = st.DeleteOutbox(ctx, o.ID)
			tlog(ctx, st, o.EventID, "send_failed", "", o.Label, "dropped: the channel is gone or off", 0)
			continue
		case failed[o.DestKey]:
			continue
		case now.Unix()-o.QueuedAt > int64(outboxKeep/time.Second):
			_ = st.DeleteOutbox(ctx, o.ID)
			tlog(ctx, st, o.EventID, "send_failed", "", o.Label, fmt.Sprintf("given up after a day and %d more tries", o.Attempts), 0)
			continue
		}
		var ev notify.Event
		if err := json.Unmarshal(o.Event, &ev); err != nil {
			_ = st.DeleteOutbox(ctx, o.ID)
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
		case "reminder":
			tlog(ctx, st, o.EventID, "reminder", "", o.Label, fmt.Sprintf("reminder %d", ev.Reminder), 0)
		case "ack":
			tlog(ctx, st, o.EventID, "ack_told", "", o.Label, o.Label+", "+late+" late", 0)
		case "recovery":
			tlog(ctx, st, o.EventID, "resolved", "", o.Label, "RESOLVED reached "+o.Label+" "+late+" late", 0)
		default:
			tlog(ctx, st, o.EventID, "alerted", "", o.Label, "sent "+late+" late", 0)
		}
		logger.Info("notifier: queued send went out", "event", o.EventID, "to", o.Label, "kind", o.Kind, "late", late)
	}
	pending := outboxPending{}
	waiting, err := st.OutboxWaiting(ctx)
	if err != nil {
		logger.Warn("notifier: load outbox", "err", err)
	}
	for _, o := range waiting {
		pending[pendingKey(o.EventID, o.DestKey)] = true
	}
	return pending
}

// toldCount words how a RESOLVED or acknowledged notice fared: "2 channels told, 1 still trying".
func toldCount(told, trying int) string {
	out := fmt.Sprintf("%d channels told", told)
	if trying > 0 {
		out += fmt.Sprintf(", %d still trying", trying)
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
