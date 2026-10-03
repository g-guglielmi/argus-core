// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// Tag is a label across sites: on a host, or on a probe (then on every host it monitors).
type Tag struct {
	Name        string
	Color       string // #rrggbb
	Description string
	CreatedAt   int64
}

// ErrTagExists is returned when a tag of that name is already there.
var ErrTagExists = errors.New("a tag of that name already exists")

// ListTags returns every tag, by name.
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, color, description, created_at FROM tags ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Name, &t.Color, &t.Description, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTag adds a tag.
func (s *Store) CreateTag(ctx context.Context, t Tag) error {
	if _, err := s.tagByName(ctx, t.Name); err == nil {
		return ErrTagExists
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tags (name, color, description, created_at) VALUES (?, ?, ?, ?)`, t.Name, t.Color, t.Description, time.Now().Unix())
	return err
}

func (s *Store) tagByName(ctx context.Context, name string) (Tag, error) {
	var t Tag
	err := s.db.QueryRowContext(ctx, `SELECT name, color, description, created_at FROM tags WHERE name = ? COLLATE NOCASE`, name).Scan(&t.Name, &t.Color, &t.Description, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// UpdateTag changes a tag's colour and description and may rename it: hosts, probes and the channels
// limited to it follow the new name.
func (s *Store) UpdateTag(ctx context.Context, oldName string, t Tag) error {
	if !strings.EqualFold(oldName, t.Name) {
		if _, err := s.tagByName(ctx, t.Name); err == nil {
			return ErrTagExists
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tags SET name = ?, color = ?, description = ? WHERE name = ?`, t.Name, t.Color, t.Description, oldName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if oldName != t.Name {
		if err := renameChannelTag(ctx, tx, oldName, t.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteTag removes a tag from everything that carries it, and from the channels limited to it.
func (s *Store) DeleteTag(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE name = ?`, name); err != nil {
		return err
	}
	if err := renameChannelTag(ctx, tx, name, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// renameChannelTag rewrites a tag in the channels limited to it ("" drops it).
func renameChannelTag(ctx context.Context, tx *sql.Tx, from, to string) error {
	for _, table := range []string{"notify_channels", "user_notify_channels"} {
		rows, err := tx.QueryContext(ctx, `SELECT id, tags FROM `+table+` WHERE tags != ''`)
		if err != nil {
			return err
		}
		type upd struct {
			id   int64
			tags string
		}
		var ups []upd
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			tags := decodeSites(raw)
			changed := false
			var out []string
			for _, t := range tags {
				if t == from {
					changed = true
					if to != "" {
						out = append(out, to)
					}
					continue
				}
				out = append(out, t)
			}
			if changed {
				ups = append(ups, upd{id, encodeSites(out)})
			}
		}
		rows.Close()
		for _, u := range ups {
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET tags = ? WHERE id = ?`, u.tags, u.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// HostTags returns each host's own tags (not the ones its probe gives it).
func (s *Store) HostTags(ctx context.Context) (map[string][]string, error) {
	return s.tagMap(ctx, `SELECT host_id, tag FROM host_tags ORDER BY tag COLLATE NOCASE`)
}

// ProbeTags returns each probe's tags, by proxy id.
func (s *Store) ProbeTags(ctx context.Context) (map[string][]string, error) {
	return s.tagMap(ctx, `SELECT proxy_id, tag FROM probe_tags ORDER BY tag COLLATE NOCASE`)
}

func (s *Store) tagMap(ctx context.Context, q string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		out[id] = append(out[id], tag)
	}
	return out, rows.Err()
}

// SetHostTags replaces a host's own tags; tags that don't exist are refused.
func (s *Store) SetHostTags(ctx context.Context, hostID string, tags []string) error {
	return s.setTags(ctx, "host_tags", "host_id", hostID, tags)
}

// SetProbeTags replaces a probe's tags.
func (s *Store) SetProbeTags(ctx context.Context, proxyID string, tags []string) error {
	return s.setTags(ctx, "probe_tags", "proxy_id", proxyID, tags)
}

func (s *Store) setTags(ctx context.Context, table, col, id string, tags []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+col+` = ?`, id); err != nil {
		return err
	}
	for _, t := range uniqueTags(tags) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (`+col+`, tag) VALUES (?, ?)`, id, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ChangeHostTags adds and removes tags on many hosts at once (the bulk action).
func (s *Store) ChangeHostTags(ctx context.Context, hostIDs, add, remove []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, h := range hostIDs {
		for _, t := range uniqueTags(remove) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM host_tags WHERE host_id = ? AND tag = ?`, h, t); err != nil {
				return err
			}
		}
		for _, t := range uniqueTags(add) {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO host_tags (host_id, tag) VALUES (?, ?)`, h, t); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func uniqueTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
