// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"time"
)

// Runbook is what someone should do when a sensor is in trouble (server/runbooks.go): for one sensor
// (Scope "item", ItemID) or for every sensor of a kind on hosts of a class (Scope "kind": ClassID and
// KeyKind, the item key with its instance as "*"). Label names what it covers, as it read when saved.
type Runbook struct {
	ID        int64
	Scope     string
	ItemID    string
	ClassID   string
	KeyKind   string
	Label     string
	Text      string
	ByName    string
	UpdatedAt int64
}

// Runbooks returns every runbook.
func (s *Store) Runbooks(ctx context.Context) ([]Runbook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, scope, item_id, class_id, key_kind, label, text, by_name, updated_at FROM runbooks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runbook
	for rows.Next() {
		var r Runbook
		if err := rows.Scan(&r.ID, &r.Scope, &r.ItemID, &r.ClassID, &r.KeyKind, &r.Label, &r.Text, &r.ByName, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRunbook stores a runbook, replacing the one for the same sensor or kind.
func (s *Store) SaveRunbook(ctx context.Context, r Runbook) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM runbooks WHERE scope=? AND item_id=? AND class_id=? AND key_kind=?`, r.Scope, r.ItemID, r.ClassID, r.KeyKind); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runbooks(scope, item_id, class_id, key_kind, label, text, by_name, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		r.Scope, r.ItemID, r.ClassID, r.KeyKind, r.Label, r.Text, r.ByName, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRunbook removes the runbook for a sensor or a kind.
func (s *Store) DeleteRunbook(ctx context.Context, scope, itemID, classID, keyKind string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runbooks WHERE scope=? AND item_id=? AND class_id=? AND key_kind=?`, scope, itemID, classID, keyKind)
	return err
}
