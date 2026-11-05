// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
)

// SetProbeKit turns a probe into a field kit or back into a probe like any other. ErrNotFound when
// the probe has no check-in credential: it never checks in, so it could never take the address.
func (s *Store) SetProbeKit(ctx context.Context, proxyName string, kit bool) error {
	v := 0
	if kit {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET kit=? WHERE proxy_name=?`, v, proxyName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProbeIsKit says whether a probe is a field kit (false for an unknown one).
func (s *Store) ProbeIsKit(ctx context.Context, proxyName string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT kit FROM probe_agents WHERE proxy_name=?`, proxyName).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// ProbeKits returns the names of the probes that are field kits.
func (s *Store) ProbeKits(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name FROM probe_agents WHERE kit<>0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}
