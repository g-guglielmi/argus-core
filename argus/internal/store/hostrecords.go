// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
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

// ForgetDeletedHosts forgets, besides DeleteHostRecords, what Argus keeps on the sensors of hosts
// someone deleted: their priorities, sensor states (paused, hidden, alerts off), uptime days, not
// supported clocks, sensor runbooks, renamed links and kept readings, the hosts' uptime targets and
// their places in saved reports. The history (incidents, alerts sent, the change log) stays.
func (s *Store) ForgetDeletedHosts(ctx context.Context, hostIDs, itemIDs []string) error {
	if err := s.DeleteHostRecords(ctx, hostIDs); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	inList := func(ids []string) (string, []any) {
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		return "(?" + strings.Repeat(",?", len(ids)-1) + ")", args
	}
	for start := 0; start < len(itemIDs); start += 500 {
		in, args := inList(itemIDs[start:min(start+500, len(itemIDs))])
		for _, q := range []string{
			`DELETE FROM item_priority WHERE item_id IN ` + in,
			`DELETE FROM suppressions WHERE scope='item' AND target_id IN ` + in,
			`DELETE FROM uptime_days WHERE item_id IN ` + in,
			`DELETE FROM item_unsupported WHERE item_id IN ` + in,
			`DELETE FROM runbooks WHERE item_id IN ` + in,
			`DELETE FROM sensor_archive WHERE item_id IN ` + in,
		} {
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
	}
	if len(hostIDs) > 0 {
		in, args := inList(hostIDs)
		for _, q := range []string{
			`DELETE FROM uptime_targets WHERE scope<>'site' AND key IN ` + in,
			`DELETE FROM sensor_renames WHERE host_id IN ` + in,
		} {
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
	}
	// Saved reports: the hosts and their picked sensors leave them.
	gone := map[string]bool{}
	for _, id := range hostIDs {
		gone[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, spec FROM reports`)
	if err != nil {
		return err
	}
	type rep struct {
		id   int64
		spec string
	}
	var reps []rep
	for rows.Next() {
		var r rep
		if err := rows.Scan(&r.id, &r.spec); err != nil {
			rows.Close()
			return err
		}
		reps = append(reps, r)
	}
	rows.Close()
	for _, r := range reps {
		var sp ReportSpec
		if json.Unmarshal([]byte(r.spec), &sp) != nil {
			continue
		}
		hosts := sp.Hosts[:0:0]
		for _, h := range sp.Hosts {
			if !gone[h] {
				hosts = append(hosts, h)
			}
		}
		sensors := sp.Sensors[:0:0]
		for _, x := range sp.Sensors {
			if !gone[x.HostID] {
				sensors = append(sensors, x)
			}
		}
		if len(hosts) == len(sp.Hosts) && len(sensors) == len(sp.Sensors) {
			continue
		}
		sp.Hosts, sp.Sensors = hosts, sensors
		b, err := json.Marshal(sp)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reports SET spec=?, updated_at=? WHERE id=?`, string(b), time.Now().Unix(), r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
