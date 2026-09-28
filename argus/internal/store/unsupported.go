// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"time"
)

// SyncUnsupported records which sensors are "not supported" right now and returns since when each has
// been: a sensor seen for the first time is stamped now, and sensors that collect again are forgotten.
func (s *Store) SyncUnsupported(ctx context.Context, itemIDs []string) (map[string]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT item_id, since FROM item_unsupported`)
	if err != nil {
		return nil, err
	}
	known := map[string]int64{}
	for rows.Next() {
		var id string
		var since int64
		if err := rows.Scan(&id, &since); err != nil {
			rows.Close()
			return nil, err
		}
		known[id] = since
	}
	rows.Close()
	now := time.Now().Unix()
	out := make(map[string]int64, len(itemIDs))
	for _, id := range itemIDs {
		since, ok := known[id]
		if !ok {
			since = now
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_unsupported(item_id, since) VALUES(?, ?)`, id, since); err != nil {
				return nil, err
			}
		}
		out[id] = since
	}
	for id := range known {
		if _, still := out[id]; !still {
			if _, err := tx.ExecContext(ctx, `DELETE FROM item_unsupported WHERE item_id=?`, id); err != nil {
				return nil, err
			}
		}
	}
	return out, tx.Commit()
}

// UnsupportedSince returns since when each tracked sensor has been "not supported" (read-only).
func (s *Store) UnsupportedSince(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, since FROM item_unsupported`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var since int64
		if err := rows.Scan(&id, &since); err != nil {
			return nil, err
		}
		out[id] = since
	}
	return out, rows.Err()
}
