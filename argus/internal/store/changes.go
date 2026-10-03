// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Change is one entry of the change log: who changed what and when, with the values before and
// after when the change has them. One action is one entry, however many hosts it touched.
type Change struct {
	ID        int64
	At        int64
	ActorKind string // user | token | argus | link
	ActorID   int64
	Actor     string // who, as they were named then
	Category  string // hosts | states | thresholds | groups | maintenance | discovery | probes | channels | users | statuspages | updates | settings
	Action    string // "Paused a host", "Changed a threshold", ...
	Object    string // what it was done to, as named then: "web1", "web1 · CPU utilization"
	Detail    string
	Diff      []ChangeDiff
	Reason    string
	RequestID string
	HostIDs   []string // the hosts it touched, for a host's own list and the probe / group filters
}

// ChangeDiff is one value a change moved from Old to New ("" = it wasn't set).
type ChangeDiff struct {
	Field string `json:"f"`
	Old   string `json:"o"`
	New   string `json:"n"`
}

// AddChanges writes entries to the change log.
func (s *Store) AddChanges(ctx context.Context, cs []Change) error {
	if len(cs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range cs {
		diff := ""
		if len(c.Diff) > 0 {
			b, _ := json.Marshal(c.Diff)
			diff = string(b)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO changes (at, actor_kind, actor_id, actor, category, action, object, detail, diff, reason, request_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.At, c.ActorKind, c.ActorID, c.Actor, c.Category, c.Action, c.Object, c.Detail, diff, c.Reason, c.RequestID)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		seen := map[string]bool{}
		for _, h := range c.HostIDs {
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO change_hosts (change_id, host_id) VALUES (?, ?)`, id, h); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ChangeQuery narrows a change log read. Zero values don't filter.
type ChangeQuery struct {
	From     int64    // at or after (unix seconds)
	Before   int64    // only ids below this (paging back)
	HostIDs  []string // touched one of these hosts; nil = any (an empty non-nil slice matches nothing)
	Category string
	Text     string // in the action, object, actor, detail or reason (case-insensitive)
	Limit    int
}

const changeCols = `id, at, actor_kind, actor_id, actor, category, action, object, detail, diff, reason, request_id`

// ListChanges returns the newest entries matching q, newest first, each with its hosts.
func (s *Store) ListChanges(ctx context.Context, q ChangeQuery) ([]Change, error) {
	if q.HostIDs != nil && len(q.HostIDs) == 0 {
		return []Change{}, nil
	}
	where := []string{"1=1"}
	var args []any
	if q.From > 0 {
		where = append(where, "at >= ?")
		args = append(args, q.From)
	}
	if q.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	if q.Category != "" {
		where = append(where, "category = ?")
		args = append(args, q.Category)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		like := "%" + strings.ToLower(t) + "%"
		where = append(where, "(lower(action) LIKE ? OR lower(object) LIKE ? OR lower(actor) LIKE ? OR lower(detail) LIKE ? OR lower(reason) LIKE ? OR lower(diff) LIKE ?)")
		args = append(args, like, like, like, like, like, like)
	}
	if q.HostIDs != nil {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(q.HostIDs)), ",")
		where = append(where, "id IN (SELECT change_id FROM change_hosts WHERE host_id IN ("+ph+"))")
		for _, h := range q.HostIDs {
			args = append(args, h)
		}
	}
	limit := q.Limit
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+changeCols+` FROM changes WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []Change
	byID := map[int64]int{}
	for rows.Next() {
		var c Change
		var diff string
		if err := rows.Scan(&c.ID, &c.At, &c.ActorKind, &c.ActorID, &c.Actor, &c.Category, &c.Action, &c.Object, &c.Detail, &diff, &c.Reason, &c.RequestID); err != nil {
			rows.Close()
			return nil, err
		}
		if diff != "" {
			_ = json.Unmarshal([]byte(diff), &c.Diff)
		}
		byID[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []Change{}, nil
	}
	// Each entry's hosts, in one read.
	ph := strings.TrimSuffix(strings.Repeat("?,", len(out)), ",")
	ids := make([]any, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	hr, err := s.db.QueryContext(ctx, `SELECT change_id, host_id FROM change_hosts WHERE change_id IN (`+ph+`) ORDER BY rowid`, ids...)
	if err != nil {
		return nil, err
	}
	defer hr.Close()
	for hr.Next() {
		var id int64
		var h string
		if err := hr.Scan(&id, &h); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].HostIDs = append(out[i].HostIDs, h)
		}
	}
	return out, hr.Err()
}

// PruneChanges drops entries older than before (unix seconds).
func (s *Store) PruneChanges(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM changes WHERE at < ?`, before)
	return err
}
