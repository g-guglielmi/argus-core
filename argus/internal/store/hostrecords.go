// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"strings"
)

// hostTables are the tables whose rows belong to one host and go with it. The history (alerts sent,
// incidents, the change log) stays: it says what happened, host gone or not.
var hostTables = []string{
	"device_class", "host_masters", "host_tags", "host_facts", "host_links", "host_journal",
	"host_domains", "host_upstream", "push_sensors", "push_hosts", "sensor_notes",
}

// DeleteHostRecords forgets what Argus keeps on hosts that were removed from Zabbix (a field kit's,
// once it has been away long enough): their rows in the host tables and their host suppressions.
// A discovered device that had become one of them is offered again, as new.
func (s *Store) DeleteHostRecords(ctx context.Context, hostIDs []string) error {
	if len(hostIDs) == 0 {
		return nil
	}
	in := "(?" + strings.Repeat(",?", len(hostIDs)-1) + ")"
	args := make([]any, len(hostIDs))
	for i, id := range hostIDs {
		args[i] = id
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range hostTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE host_id IN `+in, args...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM suppressions WHERE scope='host' AND target_id IN `+in, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discovery_results SET state='new', host_id='' WHERE host_id IN `+in, args...); err != nil {
		return err
	}
	return tx.Commit()
}
