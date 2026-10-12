// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Report is a saved PDF report (server/reports.go): what it covers, over which period, and when it
// is emailed to whom. OwnerID is the user who last saved it: a scheduled report covers what that
// user can see.
type Report struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Spec     ReportSpec     `json:"spec"`
	Schedule ReportSchedule `json:"schedule"`
	OwnerID  int64          `json:"owner_id"`
	// When it was made and emailed last, and why the last try failed ("" when it didn't).
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LastMadeAt int64  `json:"last_made_at"`
	LastSentAt int64  `json:"last_sent_at"`
	LastError  string `json:"last_error"`
	// The scheduler's own marks: the last occurrence it dealt with (sent, or given up on) and its
	// last try, so a failed send is tried again a while later, not every minute.
	LastRunAt int64 `json:"-"`
	LastTryAt int64 `json:"-"`
}

// ReportSpec is what a report covers. A site covers every host in it, now and later; a host its
// uptime and problems; a sensor its chart (its channels, for a group).
type ReportSpec struct {
	Sites   []string       `json:"sites"`
	Hosts   []string       `json:"hosts"`
	Sensors []ReportSensor `json:"sensors"`
	// Period is "7d", "30d", "last_month", "this_month" or "custom" (From and To, YYYY-MM-DD, both
	// days included).
	Period          string  `json:"period"`
	From            string  `json:"from,omitempty"`
	To              string  `json:"to,omitempty"`
	Target          float64 `json:"target"` // uptime target in percent, 0: none
	SkipMaintenance bool    `json:"skip_maintenance"`
}

// ReportSensor is a sensor picked for its chart: one item, or a group's channels in their order.
type ReportSensor struct {
	HostID string   `json:"host_id"`
	Items  []string `json:"items"`
	Label  string   `json:"label"` // its name when it was picked, for a sensor that is gone since
}

// ReportSchedule is when a report is emailed by itself: Every is "" (only on request), "day", "week"
// (on Weekday, 0 = Sunday) or "month" (on Day, 1-28, or -1 for the last day), at Minute (minutes
// after midnight, Argus's timezone).
type ReportSchedule struct {
	Every   string   `json:"every"`
	Weekday int      `json:"weekday"`
	Day     int      `json:"day"`
	Minute  int      `json:"minute"`
	To      []string `json:"to"`
	Enabled bool     `json:"enabled"`
}

const reportColumns = `id,name,spec,schedule,owner_id,created_at,updated_at,last_made_at,last_sent_at,last_error,last_run_at,last_try_at`

func scanReport(row rowScanner) (Report, error) {
	var r Report
	var spec, sched string
	err := row.Scan(&r.ID, &r.Name, &spec, &sched, &r.OwnerID, &r.CreatedAt, &r.UpdatedAt, &r.LastMadeAt, &r.LastSentAt, &r.LastError, &r.LastRunAt, &r.LastTryAt)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(spec), &r.Spec)
	_ = json.Unmarshal([]byte(sched), &r.Schedule)
	return r, nil
}

// Reports lists every saved report, by name.
func (s *Store) Reports(ctx context.Context) ([]Report, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reportColumns+` FROM reports ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Report returns one saved report (ErrNotFound when there is none).
func (s *Store) Report(ctx context.Context, id int64) (Report, error) {
	r, err := scanReport(s.db.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// CreateReport stores a new report and returns its id.
func (s *Store) CreateReport(ctx context.Context, r Report) (int64, error) {
	spec, _ := json.Marshal(r.Spec)
	sched, _ := json.Marshal(r.Schedule)
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO reports(name,spec,schedule,owner_id,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		r.Name, string(spec), string(sched), r.OwnerID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateReport replaces a report's name, contents and schedule; the one saving it becomes its owner.
// The scheduler starts over from now: a schedule just saved doesn't send for an hour that has passed.
func (s *Store) UpdateReport(ctx context.Context, r Report) error {
	spec, _ := json.Marshal(r.Spec)
	sched, _ := json.Marshal(r.Schedule)
	res, err := s.db.ExecContext(ctx, `UPDATE reports SET name=?,spec=?,schedule=?,owner_id=?,updated_at=?,last_try_at=0 WHERE id=?`,
		r.Name, string(spec), string(sched), r.OwnerID, time.Now().Unix(), r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteReport removes a report.
func (s *Store) DeleteReport(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id=?`, id)
	return err
}

// MarkReportMade records that a report was made (downloaded or emailed) at.
func (s *Store) MarkReportMade(ctx context.Context, id, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET last_made_at=? WHERE id=?`, at, id)
	return err
}

// MarkReportSent records an email: sent at (with errText "") or failed (errText says why). run is
// the occurrence of the schedule it settles (0: a send on request, which settles none).
func (s *Store) MarkReportSent(ctx context.Context, id, at, run int64, errText string) error {
	if errText == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE reports SET last_sent_at=?,last_made_at=?,last_error='',last_run_at=MAX(last_run_at,?),last_try_at=? WHERE id=?`, at, at, run, at, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET last_error=?,last_run_at=MAX(last_run_at,?),last_try_at=? WHERE id=?`, errText, run, at, id)
	return err
}
