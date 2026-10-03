// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"strings"
)

// SensorNote is a note on a sensor in trouble: shown with the sensor and sent with its alerts until
// the sensor is OK again, then kept (cleared) for its incident history.
type SensorNote struct {
	ID        int64
	Key       string // the sensor's item id, or an Argus-raised row's problem id
	HostID    string
	Text      string
	ByUser    int64
	ByName    string // who wrote it, as they were named then
	CreatedAt int64
	UpdatedAt int64
	OKSince   int64 // when the sensor was first seen with no open problem; 0 = it has one
	ClearedAt int64 // 0 = shown
}

const sensorNoteCols = `id, sensor_key, host_id, text, by_user, by_name, created_at, updated_at, ok_since, cleared_at`

func scanSensorNote(sc interface{ Scan(...any) error }) (SensorNote, error) {
	var n SensorNote
	err := sc.Scan(&n.ID, &n.Key, &n.HostID, &n.Text, &n.ByUser, &n.ByName, &n.CreatedAt, &n.UpdatedAt, &n.OKSince, &n.ClearedAt)
	return n, err
}

// SetSensorNote writes the note shown on a sensor: it replaces the text of the one already shown, or
// starts a new one. Writing it counts as "the sensor still needs it", so the OK countdown restarts.
func (s *Store) SetSensorNote(ctx context.Context, key, hostID, text string, byUser int64, byName string, now int64) (SensorNote, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SensorNote{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE sensor_notes SET text=?, host_id=?, by_user=?, by_name=?, updated_at=?, ok_since=0
		WHERE sensor_key=? AND cleared_at=0`, text, hostID, byUser, byName, now, key)
	if err != nil {
		return SensorNote{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sensor_notes (sensor_key, host_id, text, by_user, by_name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, key, hostID, text, byUser, byName, now, now); err != nil {
			return SensorNote{}, err
		}
	}
	n, err := scanSensorNote(tx.QueryRowContext(ctx, `SELECT `+sensorNoteCols+` FROM sensor_notes WHERE sensor_key=? AND cleared_at=0`, key))
	if err != nil {
		return SensorNote{}, err
	}
	return n, tx.Commit()
}

// ClearSensorNote takes a sensor's note off it (kept for the history), as of at.
func (s *Store) ClearSensorNote(ctx context.Context, key string, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sensor_notes SET cleared_at=? WHERE sensor_key=? AND cleared_at=0`, at, key)
	return err
}

// SetSensorNoteOKSince records when a noted sensor was first seen OK (0: it has a problem again).
func (s *Store) SetSensorNoteOKSince(ctx context.Context, id, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sensor_notes SET ok_since=? WHERE id=?`, at, id)
	return err
}

// LiveSensorNotes returns the notes shown right now, by sensor key.
func (s *Store) LiveSensorNotes(ctx context.Context) (map[string]SensorNote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sensorNoteCols+` FROM sensor_notes WHERE cleared_at=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SensorNote{}
	for rows.Next() {
		n, err := scanSensorNote(rows)
		if err != nil {
			return nil, err
		}
		out[n.Key] = n
	}
	return out, rows.Err()
}

// SensorNotesSince returns the notes on these sensors that were shown at some point since `from`
// (still shown, or cleared after it), oldest first, for the incident history.
func (s *Store) SensorNotesSince(ctx context.Context, keys []string, from int64) ([]SensorNote, error) {
	var out []SensorNote
	for start := 0; start < len(keys); start += 500 {
		chunk := keys[start:min(start+500, len(keys))]
		args := []any{from}
		for _, k := range chunk {
			args = append(args, k)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+sensorNoteCols+` FROM sensor_notes
			WHERE (cleared_at=0 OR cleared_at>=?) AND sensor_key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)
			ORDER BY created_at, id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			n, err := scanSensorNote(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PruneSensorNotes drops notes cleared before `before`.
func (s *Store) PruneSensorNotes(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sensor_notes WHERE cleared_at>0 AND cleared_at<?`, before)
	return err
}
