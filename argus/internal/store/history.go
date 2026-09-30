// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"strings"
)

// UptimeDay is one sensor's uptime over one local calendar day: Up / Num is the fraction of checks
// that found it up (Num = 0: no data that day).
type UptimeDay struct {
	ItemID string
	Day    string // YYYY-MM-DD
	Up     float64
	Num    int64
}

// UptimeDays returns the stored days of the given sensors from day `from` on (inclusive).
func (s *Store) UptimeDays(ctx context.Context, itemIDs []string, from string) ([]UptimeDay, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	var out []UptimeDay
	for start := 0; start < len(itemIDs); start += 500 {
		end := min(start+500, len(itemIDs))
		chunk := itemIDs[start:end]
		args := make([]any, 0, len(chunk)+1)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, from)
		rows, err := s.db.QueryContext(ctx, `SELECT item_id, day, up, num FROM uptime_days WHERE item_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`) AND day>=?`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var d UptimeDay
			if err := rows.Scan(&d.ItemID, &d.Day, &d.Up, &d.Num); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PutUptimeDays stores complete days (a day is written once; a rewrite replaces it).
func (s *Store) PutUptimeDays(ctx context.Context, days []UptimeDay) error {
	if len(days) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, d := range days {
		if _, err := tx.ExecContext(ctx, `INSERT INTO uptime_days(item_id, day, up, num) VALUES(?,?,?,?)
			ON CONFLICT(item_id, day) DO UPDATE SET up=excluded.up, num=excluded.num`, d.ItemID, d.Day, d.Up, d.Num); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneUptimeDays drops days before `before` (YYYY-MM-DD).
func (s *Store) PruneUptimeDays(ctx context.Context, before string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM uptime_days WHERE day<?`, before)
	return err
}

// ArgusIncident is one Argus-raised problem as it opened and (EndedAt > 0) closed.
type ArgusIncident struct {
	ID        int64
	EventID   string
	HostID    string
	HostName  string
	ItemID    string
	Name      string
	Severity  int
	Reason    string
	StartedAt int64
	EndedAt   int64
}

// OpenArgusIncident is a synthetic problem that is open right now, as the notifier sees it.
type OpenArgusIncident struct {
	EventID, HostID, HostName, ItemID, Name, Reason string
	Severity                                        int
	StartedAt                                       int64
}

// SyncArgusIncidents records the Argus-raised problems open right now: one that isn't logged as open
// yet gets a row, and a logged one that is no longer open is closed at `now`. The reason is kept
// from the moment the incident opened (later readings don't overwrite what it was about).
func (s *Store) SyncArgusIncidents(ctx context.Context, open []OpenArgusIncident, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, event_id FROM argus_incidents WHERE ended_at=0`)
	if err != nil {
		return err
	}
	logged := map[string]int64{}
	for rows.Next() {
		var id int64
		var ev string
		if err := rows.Scan(&id, &ev); err != nil {
			rows.Close()
			return err
		}
		logged[ev] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	current := map[string]bool{}
	for _, o := range open {
		current[o.EventID] = true
		if _, ok := logged[o.EventID]; ok {
			continue
		}
		start := o.StartedAt
		if start <= 0 || start > now {
			start = now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO argus_incidents(event_id, host_id, host_name, item_id, name, severity, reason, started_at)
			VALUES(?,?,?,?,?,?,?,?)`, o.EventID, o.HostID, o.HostName, o.ItemID, o.Name, o.Severity, o.Reason, start); err != nil {
			return err
		}
	}
	for ev, id := range logged {
		if !current[ev] {
			if _, err := tx.ExecContext(ctx, `UPDATE argus_incidents SET ended_at=? WHERE id=?`, now, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ArgusIncidents returns the logged incidents that started since `from` or are still open, newest
// first, for the given hosts (all when hostIDs is empty), at most limit.
func (s *Store) ArgusIncidents(ctx context.Context, hostIDs []string, from int64, limit int) ([]ArgusIncident, error) {
	q := `SELECT id, event_id, host_id, host_name, item_id, name, severity, reason, started_at, ended_at
		FROM argus_incidents WHERE (started_at>=? OR ended_at=0 OR ended_at>=?)`
	args := []any{from, from}
	if len(hostIDs) > 0 {
		q += ` AND host_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(hostIDs)), ",") + `)`
		for _, h := range hostIDs {
			args = append(args, h)
		}
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArgusIncident
	for rows.Next() {
		var a ArgusIncident
		if err := rows.Scan(&a.ID, &a.EventID, &a.HostID, &a.HostName, &a.ItemID, &a.Name, &a.Severity, &a.Reason, &a.StartedAt, &a.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneArgusIncidents drops closed incidents that ended before `before`.
func (s *Store) PruneArgusIncidents(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM argus_incidents WHERE ended_at>0 AND ended_at<?`, before)
	return err
}

// AckRecord is who acknowledged an event, when, and their note (By = 0: the signed link in an alert).
type AckRecord struct {
	By   int64
	Note string
	At   int64
}

// AckRecords returns the acknowledgements still on record for the given events.
func (s *Store) AckRecords(ctx context.Context, eventIDs []string) (map[string]AckRecord, error) {
	out := map[string]AckRecord{}
	for start := 0; start < len(eventIDs); start += 500 {
		end := min(start+500, len(eventIDs))
		chunk := eventIDs[start:end]
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT target_id, by_user, note, created_at FROM suppressions
			WHERE kind='ack' AND scope='event' AND target_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var by sql.NullInt64
			var r AckRecord
			if err := rows.Scan(&id, &by, &r.Note, &r.At); err != nil {
				rows.Close()
				return nil, err
			}
			r.By = by.Int64
			out[id] = r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
