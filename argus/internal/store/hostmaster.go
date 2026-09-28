// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import "context"

// host_masters is an Argus overlay: a host's master sensor, when it isn't the default (the ICMP ping
// sensor). While the master is down the notifier holds the host's other alerts. item_id "" means the
// host has no master at all.

// HostMasters returns host id -> master item id for every host with an override ("" = no master).
func (s *Store) HostMasters(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, item_id FROM host_masters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var host, item string
		if err := rows.Scan(&host, &item); err != nil {
			return nil, err
		}
		out[host] = item
	}
	return out, rows.Err()
}

// HostMaster returns a host's master override and true, or ("", false) when it uses the default.
func (s *Store) HostMaster(ctx context.Context, hostID string) (string, bool, error) {
	m, err := s.HostMasters(ctx)
	if err != nil {
		return "", false, err
	}
	item, ok := m[hostID]
	return item, ok, nil
}

// SetHostMaster overrides a host's master sensor (itemID "" = no master).
func (s *Store) SetHostMaster(ctx context.Context, hostID, itemID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO host_masters(host_id, item_id) VALUES(?, ?) ON CONFLICT(host_id) DO UPDATE SET item_id=excluded.item_id`,
		hostID, itemID)
	return err
}

// ClearHostMaster returns a host to the default master sensor.
func (s *Store) ClearHostMaster(ctx context.Context, hostID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM host_masters WHERE host_id=?`, hostID)
	return err
}
