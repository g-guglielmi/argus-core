// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SensorRename is a sensor its discovery lost under one name and found again under another
// (server/renames.go): linked to its new sensor (NewID), or kept apart (NewID "").
type SensorRename struct {
	OldID   string
	NewID   string
	HostID  string
	OldName string
	OldKey  string
	Until   int64 // when discovery last found the old name
	ByName  string
	At      int64
}

// SensorRenames returns every rename decision.
func (s *Store) SensorRenames(ctx context.Context) ([]SensorRename, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT old_id, new_id, host_id, old_name, old_key, until, by_name, at FROM sensor_renames ORDER BY at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SensorRename
	for rows.Next() {
		var r SensorRename
		if err := rows.Scan(&r.OldID, &r.NewID, &r.HostID, &r.OldName, &r.OldKey, &r.Until, &r.ByName, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutSensorRename stores a decision about an old sensor, replacing an earlier one.
func (s *Store) PutSensorRename(ctx context.Context, r SensorRename) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sensor_renames(old_id, new_id, host_id, old_name, old_key, until, by_name, at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(old_id) DO UPDATE SET new_id=excluded.new_id, host_id=excluded.host_id, old_name=excluded.old_name,
		old_key=excluded.old_key, until=excluded.until, by_name=excluded.by_name, at=excluded.at`,
		r.OldID, r.NewID, r.HostID, r.OldName, r.OldKey, r.Until, r.ByName, r.At)
	return err
}

// ArchPoint is one archived raw reading of an old sensor.
type ArchPoint struct {
	ItemID string
	Clock  int64
	V      float64
}

// ArchTrend is one archived hour of an old sensor: min, average and max over Num checks.
type ArchTrend struct {
	ItemID        string
	Clock         int64
	Min, Avg, Max float64
	Num           int64
}

// PutSensorArchive stores an old sensor's readings (a second copy of the same moment is ignored).
func (s *Store) PutSensorArchive(ctx context.Context, hist []ArchPoint, trends []ArchTrend) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	h, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO sensor_archive(item_id, kind, clock, v) VALUES(?,0,?,?)`)
	if err != nil {
		return err
	}
	defer h.Close()
	for _, p := range hist {
		if _, err := h.ExecContext(ctx, p.ItemID, p.Clock, p.V); err != nil {
			return err
		}
	}
	t, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO sensor_archive(item_id, kind, clock, mn, av, mx, num) VALUES(?,1,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer t.Close()
	for _, p := range trends {
		if _, err := t.ExecContext(ctx, p.ItemID, p.Clock, p.Min, p.Avg, p.Max, p.Num); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func inList(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// ArchivedHistory returns the archived raw readings of the given old sensors within [from, to].
func (s *Store) ArchivedHistory(ctx context.Context, ids []string, from, to int64) ([]ArchPoint, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, from, to)
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, clock, v FROM sensor_archive WHERE item_id IN (`+inList(len(ids))+`) AND kind=0 AND clock>=? AND clock<=? ORDER BY clock`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchPoint
	for rows.Next() {
		var p ArchPoint
		if err := rows.Scan(&p.ItemID, &p.Clock, &p.V); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ArchivedTrends returns the archived hours of the given old sensors within [from, to].
func (s *Store) ArchivedTrends(ctx context.Context, ids []string, from, to int64) ([]ArchTrend, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, from, to)
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, clock, mn, av, mx, num FROM sensor_archive WHERE item_id IN (`+inList(len(ids))+`) AND kind=1 AND clock>=? AND clock<=? ORDER BY clock`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchTrend
	for rows.Next() {
		var p ArchTrend
		if err := rows.Scan(&p.ItemID, &p.Clock, &p.Min, &p.Avg, &p.Max, &p.Num); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneSensorArchive drops archived raw readings before histBefore and hours before trendBefore, and
// the links whose old sensor has nothing archived left.
func (s *Store) PruneSensorArchive(ctx context.Context, histBefore, trendBefore int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sensor_archive WHERE (kind=0 AND clock<?) OR (kind=1 AND clock<?)`, histBefore, trendBefore); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sensor_renames WHERE at<? AND old_id NOT IN (SELECT DISTINCT item_id FROM sensor_archive)`, trendBefore)
	return err
}

// MoveSensor hands what Argus keeps about an old sensor to the one it was renamed to: its priority,
// notes, runbook, actions and their runs, master role, hidden state, logged incidents and report
// entries. What the new sensor already has of its own stays. Its stored uptime days go, so they are
// read again with the old sensor's readings.
func (s *Store) MoveSensor(ctx context.Context, oldID, newID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`UPDATE OR IGNORE item_priority SET item_id=? WHERE item_id=?`,
		`UPDATE sensor_notes SET sensor_key=? WHERE sensor_key=?`,
		`UPDATE OR IGNORE runbooks SET item_id=? WHERE scope='item' AND item_id=?`,
		`UPDATE actions SET item_id=? WHERE scope='item' AND item_id=?`,
		`UPDATE action_runs SET item_id=? WHERE item_id=?`,
		`UPDATE host_masters SET item_id=? WHERE item_id=?`,
		`UPDATE OR IGNORE suppressions SET target_id=? WHERE scope='item' AND kind='hide' AND target_id=?`,
		`UPDATE argus_incidents SET item_id=? WHERE item_id=?`,
		`UPDATE notify_events SET item_id=? WHERE item_id=?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, newID, oldID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uptime_days WHERE item_id=?`, newID); err != nil {
		return err
	}
	// Report entries: a sensor picked for its chart follows its new name.
	rows, err := tx.QueryContext(ctx, `SELECT id, spec FROM reports`)
	if err != nil {
		return err
	}
	type rep struct {
		id   int64
		spec string
	}
	var reps []rep
	for rows.Next() {
		var r rep
		if err := rows.Scan(&r.id, &r.spec); err != nil {
			rows.Close()
			return err
		}
		reps = append(reps, r)
	}
	rows.Close()
	for _, r := range reps {
		var sp ReportSpec
		if json.Unmarshal([]byte(r.spec), &sp) != nil {
			continue
		}
		changed := false
		for i := range sp.Sensors {
			for j, id := range sp.Sensors[i].Items {
				if id == oldID {
					sp.Sensors[i].Items[j] = newID
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		b, err := json.Marshal(sp)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reports SET spec=?, updated_at=? WHERE id=?`, string(b), time.Now().Unix(), r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PutArchivedIncidents keeps an old sensor's closed incidents, now on its new sensor (kind
// "renamed"): Zabbix deletes them with the old sensor's triggers. An incident kept already is left.
func (s *Store) PutArchivedIncidents(ctx context.Context, in []ArgusIncident) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range in {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM argus_incidents WHERE event_id=?`, a.EventID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO argus_incidents(event_id, host_id, host_name, item_id, name, severity, reason, started_at, ended_at, kind)
			VALUES(?,?,?,?,?,?,?,?,?,'renamed')`, a.EventID, a.HostID, a.HostName, a.ItemID, a.Name, a.Severity, a.Reason, a.StartedAt, a.EndedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
