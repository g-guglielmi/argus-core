// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import "context"

// Outbox is one send that failed (or waits behind one that did) and goes again later: the event as
// the notifier built it, for one destination of one incident. A destination's recap of the
// incidents that came and went while it could not be reached is a row with no incident (EventID "").
type Outbox struct {
	ID       int64
	EventID  string
	DestKey  string // DeliveryKey of the destination
	Kind     string // the event's kind: problem | reminder | ack | recovery | info (a recap)
	Label    string // how the incident timeline names the destination
	Event    []byte // the notify.Event, JSON
	Attempts int    // retries so far
	QueuedAt int64  // unix s
	NextAt   int64  // unix s: not retried before
}

const outboxCols = `id,event_id,dest_key,kind,label,event,attempts,queued_at,next_at`

func scanOutbox(rows interface{ Scan(...any) error }) (Outbox, error) {
	var o Outbox
	err := rows.Scan(&o.ID, &o.EventID, &o.DestKey, &o.Kind, &o.Label, &o.Event, &o.Attempts, &o.QueuedAt, &o.NextAt)
	return o, err
}

// QueueOutbox adds a send to the outbox.
func (s *Store) QueueOutbox(ctx context.Context, o Outbox) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_outbox(event_id,dest_key,kind,label,event,attempts,queued_at,next_at) VALUES(?,?,?,?,?,?,?,?)`,
		o.EventID, o.DestKey, o.Kind, o.Label, o.Event, o.Attempts, o.QueuedAt, o.NextAt)
	return err
}

func (s *Store) outboxQuery(ctx context.Context, where string, args ...any) ([]Outbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+outboxCols+` FROM notify_outbox `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outbox
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OutboxAll returns every queued send, oldest first.
func (s *Store) OutboxAll(ctx context.Context) ([]Outbox, error) { return s.outboxQuery(ctx, "") }

// OutboxPair returns the sends waiting for one incident at one destination, oldest first.
func (s *Store) OutboxPair(ctx context.Context, eventID, destKey string) ([]Outbox, error) {
	return s.outboxQuery(ctx, `WHERE event_id=? AND dest_key=?`, eventID, destKey)
}

// OutboxWaiting returns every queued send without its event: which incidents and destinations
// still have one waiting, and how often each was tried.
func (s *Store) OutboxWaiting(ctx context.Context) ([]Outbox, error) {
	all, err := s.OutboxAll(ctx)
	for i := range all {
		all[i].Event = nil
	}
	return all, err
}

// OutboxRetry records a failed retry and when the next may run.
func (s *Store) OutboxRetry(ctx context.Context, id int64, attempts int, nextAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notify_outbox SET attempts=?, next_at=? WHERE id=?`, attempts, nextAt, id)
	return err
}

// UpdateOutboxEvent replaces a queued send's event (a recap that grew by an incident).
func (s *Store) UpdateOutboxEvent(ctx context.Context, id int64, event []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notify_outbox SET event=? WHERE id=?`, event, id)
	return err
}

// DeleteOutbox drops a queued send: it went out, or is given up on.
func (s *Store) DeleteOutbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_outbox WHERE id=?`, id)
	return err
}

// DeleteOutboxPair drops every send waiting for one incident at one destination.
func (s *Store) DeleteOutboxPair(ctx context.Context, eventID, destKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_outbox WHERE event_id=? AND dest_key=?`, eventID, destKey)
	return err
}
