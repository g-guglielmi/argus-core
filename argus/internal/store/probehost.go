// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
)

// ProbeHost is what Argus keeps about a probe VM's reboots (DESIGN 14c), each a small JSON document
// the server package reads and writes: the probe's own reboot window ("" = it follows the fleet's),
// its host reporter's account of Reboot now and Patch now, and the request an admin made.
type ProbeHost struct {
	Window  string
	Actions string
	Req     string
}

// ProbeHosts returns every probe's ProbeHost, keyed by proxy name.
func (s *Store) ProbeHosts(ctx context.Context) (map[string]ProbeHost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name,reboot_window,host_actions,host_req FROM probe_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProbeHost{}
	for rows.Next() {
		var name string
		var h ProbeHost
		if err := rows.Scan(&name, &h.Window, &h.Actions, &h.Req); err != nil {
			return nil, err
		}
		out[name] = h
	}
	return out, rows.Err()
}

// ProbeHostByName returns one probe's ProbeHost, ErrNotFound for an unknown probe.
func (s *Store) ProbeHostByName(ctx context.Context, name string) (ProbeHost, error) {
	var h ProbeHost
	err := s.db.QueryRowContext(ctx, `SELECT reboot_window,host_actions,host_req FROM probe_agents WHERE proxy_name=?`, name).Scan(&h.Window, &h.Actions, &h.Req)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// SetProbeRebootWindow stores a probe's own reboot window ("" puts it back on the fleet's).
func (s *Store) SetProbeRebootWindow(ctx context.Context, name, doc string) error {
	return s.setProbeHostCol(ctx, `UPDATE probe_agents SET reboot_window=? WHERE proxy_name=?`, doc, name)
}

// SetProbeHostActions stores the host reporter's account of Reboot now and Patch now.
func (s *Store) SetProbeHostActions(ctx context.Context, name, doc string) error {
	return s.setProbeHostCol(ctx, `UPDATE probe_agents SET host_actions=? WHERE proxy_name=?`, doc, name)
}

// SetProbeHostReq stores the request an admin made ("" clears it).
func (s *Store) SetProbeHostReq(ctx context.Context, name, doc string) error {
	return s.setProbeHostCol(ctx, `UPDATE probe_agents SET host_req=? WHERE proxy_name=?`, doc, name)
}

// SwapProbeHostReq replaces the request only if it is still old: the check-in that hands it out and
// the report that settles it may arrive together. False when another one changed it first.
func (s *Store) SwapProbeHostReq(ctx context.Context, name, old, doc string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET host_req=? WHERE proxy_name=? AND host_req=?`, doc, name, old)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) setProbeHostCol(ctx context.Context, q, doc, name string) error {
	res, err := s.db.ExecContext(ctx, q, doc, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
