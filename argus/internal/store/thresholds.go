// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"time"
)

// threshold_default and category_order are Argus overlays for the §D thresholds/ordering UI. See the
// table comments in store.go. Argus is the source of truth for both; the server re-applies threshold
// defaults onto the live Zabbix templates after each reconcile so a re-import can't clobber them.

// SetThresholdDefault records (or updates) a fleet-wide default for one template's threshold macro.
func (s *Store) SetThresholdDefault(ctx context.Context, template, macro, value string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO threshold_default (template, macro, value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(template, macro) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		template, macro, value, now)
	return err
}

// DeleteThresholdDefault removes a global override, reverting the macro to the template factory value.
func (s *Store) DeleteThresholdDefault(ctx context.Context, template, macro string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM threshold_default WHERE template = ? AND macro = ?`, template, macro)
	return err
}

// ThresholdDefaults returns template -> (macro -> value) for every stored global override.
func (s *Store) ThresholdDefaults(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT template, macro, value FROM threshold_default`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var tpl, macro, value string
		if err := rows.Scan(&tpl, &macro, &value); err != nil {
			return nil, err
		}
		if out[tpl] == nil {
			out[tpl] = map[string]string{}
		}
		out[tpl][macro] = value
	}
	return out, rows.Err()
}

// SetCategoryOrder replaces the ordered category list for a scope ('class:<id>' or 'host:<id>').
// An empty list clears the override (the scope falls back to its class, then the built-in profile).
func (s *Store) SetCategoryOrder(ctx context.Context, scope string, categories []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM category_order WHERE scope = ?`, scope); err != nil {
		return err
	}
	for i, cat := range categories {
		if _, err := tx.ExecContext(ctx, `INSERT INTO category_order (scope, category, ord) VALUES (?, ?, ?)`, scope, cat, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CategoryOrder returns a scope's chosen category order (lowest ord first), or nil when unset.
func (s *Store) CategoryOrder(ctx context.Context, scope string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT category FROM category_order WHERE scope = ? ORDER BY ord`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cat string
		if err := rows.Scan(&cat); err != nil {
			return nil, err
		}
		out = append(out, cat)
	}
	return out, rows.Err()
}
