// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import "context"

// Uptime targets (server/uptimetarget.go): a guaranteed uptime per calendar month, in percent, set on a
// site (for its hosts) or on a host. A host's 0 says it has none, whatever its site's.

// UptimeTargets returns the targets: the sites' by site name and the hosts' by host id.
func (s *Store) UptimeTargets(ctx context.Context) (sites, hosts map[string]float64, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope, key, pct FROM uptime_targets`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	sites, hosts = map[string]float64{}, map[string]float64{}
	for rows.Next() {
		var scope, key string
		var pct float64
		if err := rows.Scan(&scope, &key, &pct); err != nil {
			return nil, nil, err
		}
		if scope == "site" {
			sites[key] = pct
		} else {
			hosts[key] = pct
		}
	}
	return sites, hosts, rows.Err()
}

// SetUptimeTarget sets a site's or a host's target; clear removes it (a host then follows its site).
func (s *Store) SetUptimeTarget(ctx context.Context, scope, key string, pct float64, clear bool) error {
	if clear {
		_, err := s.db.ExecContext(ctx, `DELETE FROM uptime_targets WHERE scope=? AND key=?`, scope, key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO uptime_targets(scope, key, pct) VALUES(?,?,?) ON CONFLICT(scope, key) DO UPDATE SET pct=excluded.pct`, scope, key, pct)
	return err
}
