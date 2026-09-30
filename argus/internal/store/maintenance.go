// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// MaintenanceWindow is a recurring (or one-off) stretch of time during which some hosts' alerts are
// held: backups, a monthly parity check, a patch night. Collection goes on and problems stay visible;
// the notifier waits, and alerts what is still open once the window ends.
type MaintenanceWindow struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Sites   []string `json:"sites"`    // host groups it covers (a root covers its subgroups)
	HostIDs []string `json:"host_ids"` // single hosts it covers
	// Kind is "once", "daily", "weekly" or "monthly". Once uses StartAt; the others start at Minute
	// (minutes after local midnight) on the days they select: Weekdays is a bit per weekday (bit 0 =
	// Sunday), MonthDay a day of the month (1-31, a short month runs it on its last day) or -1 for
	// the last day.
	Kind        string `json:"kind"`
	StartAt     int64  `json:"start_at,omitempty"` // once: unix s
	Minute      int    `json:"minute"`
	Weekdays    int    `json:"weekdays,omitempty"`
	MonthDay    int    `json:"month_day,omitempty"`
	DurationMin int    `json:"duration_min"`
	Enabled     bool   `json:"enabled"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

const maintenanceColumns = `id,name,sites,host_ids,kind,start_at,minute,weekdays,month_day,duration_min,enabled,created_by,created_at`

func encodeIDs(ids []string) string {
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func decodeIDs(v string) []string {
	var out []string
	if strings.TrimSpace(v) != "" {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}

func scanMaintenance(row rowScanner) (MaintenanceWindow, error) {
	var w MaintenanceWindow
	var sites, hosts string
	var enabled int
	err := row.Scan(&w.ID, &w.Name, &sites, &hosts, &w.Kind, &w.StartAt, &w.Minute, &w.Weekdays, &w.MonthDay, &w.DurationMin, &enabled, &w.CreatedBy, &w.CreatedAt)
	w.Sites, w.HostIDs, w.Enabled = decodeSites(sites), decodeIDs(hosts), enabled != 0
	return w, err
}

// MaintenanceWindows lists every window, by name.
func (s *Store) MaintenanceWindows(ctx context.Context) ([]MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_windows ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceWindow
	for rows.Next() {
		w, err := scanMaintenance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// MaintenanceWindow returns one window (ErrNotFound when there is none).
func (s *Store) MaintenanceWindow(ctx context.Context, id int64) (MaintenanceWindow, error) {
	w, err := scanMaintenance(s.db.QueryRowContext(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_windows WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// CreateMaintenanceWindow stores a new window and returns its id.
func (s *Store) CreateMaintenanceWindow(ctx context.Context, w MaintenanceWindow) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO maintenance_windows(name,sites,host_ids,kind,start_at,minute,weekdays,month_day,duration_min,enabled,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.Name, encodeSites(w.Sites), encodeIDs(w.HostIDs), w.Kind, w.StartAt, w.Minute, w.Weekdays, w.MonthDay, w.DurationMin, boolInt(w.Enabled), w.CreatedBy, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMaintenanceWindow replaces a window's settings (its creator and creation time stay).
func (s *Store) UpdateMaintenanceWindow(ctx context.Context, w MaintenanceWindow) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE maintenance_windows SET name=?,sites=?,host_ids=?,kind=?,start_at=?,minute=?,weekdays=?,month_day=?,duration_min=?,enabled=? WHERE id=?`,
		w.Name, encodeSites(w.Sites), encodeIDs(w.HostIDs), w.Kind, w.StartAt, w.Minute, w.Weekdays, w.MonthDay, w.DurationMin, boolInt(w.Enabled), w.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMaintenanceWindow removes a window.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id=?`, id)
	return err
}
