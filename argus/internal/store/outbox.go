// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import "context"

// Outbox is one send that failed (or waits behind one that did) and goes again later: the event as
// the notifier built it, for one destination of one incident.
type Outbox struct {
	ID       int64
	EventID  string
	DestKey  string // DeliveryKey of the destination
	Kind     string // the event's kind: problem | reminder | ack | recovery
	Label    string // how the incident timeline names the destination
	Event    []byte // the notify.Event, JSON
	Attempts int    // retries so far
	QueuedAt int64  // unix s
	NextAt   int64  // unix s: not retried before
}

// QueueOutbox adds a send to the outbox.
func (s *Store) QueueOutbox(ctx context.Context, o Outbox) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_outbox(event_id,dest_key,kind,label,event,attempts,queued_at,next_at) VALUES(?,?,?,?,?,?,?,?)`,
		o.EventID, o.DestKey, o.Kind, o.Label, o.Event, o.Attempts, o.QueuedAt, o.NextAt)
	return err
}

// OutboxDue returns the sends whose wait is over, oldest first.
func (s *Store) OutboxDue(ctx context.Context, now int64) ([]Outbox, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,event_id,dest_key,kind,label,event,attempts,queued_at,next_at FROM notify_outbox WHERE next_at<=? ORDER BY id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outbox
	for rows.Next() {
		var o Outbox
		if err := rows.Scan(&o.ID, &o.EventID, &o.DestKey, &o.Kind, &o.Label, &o.Event, &o.Attempts, &o.QueuedAt, &o.NextAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OutboxWaiting returns every queued send, without its event: which incidents and destinations
// still have one waiting, and how often each was tried.
func (s *Store) OutboxWaiting(ctx context.Context) ([]Outbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_id,dest_key,kind,label,attempts,queued_at,next_at FROM notify_outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outbox
	for rows.Next() {
		var o Outbox
		if err := rows.Scan(&o.ID, &o.EventID, &o.DestKey, &o.Kind, &o.Label, &o.Attempts, &o.QueuedAt, &o.NextAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OutboxRetry records a failed retry and when the next may run.
func (s *Store) OutboxRetry(ctx context.Context, id int64, attempts int, nextAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notify_outbox SET attempts=?, next_at=? WHERE id=?`, attempts, nextAt, id)
	return err
}

// DeleteOutbox drops a queued send: it went out, or is given up on.
func (s *Store) DeleteOutbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_outbox WHERE id=?`, id)
	return err
}
