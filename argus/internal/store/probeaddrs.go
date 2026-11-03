// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"time"
)

// ProbeAddrs is what Argus knows of a probe's addresses (the Probes page's LAN and public line):
//   - Own: the address the probe container reaches out from (a Docker bridge one, 172.17.x, unless
//     it runs with host networking or on a macvlan), and Seen: the one Argus sees its minute report
//     come from; At is when the report last carried them.
//   - Public: the probe's own lookup of its public address, and when it was made.
//   - Host, HostGW: the host's LAN address and gateway, as a probe VM's OS report or the
//     argus-updater sidecar (reading them with the host's network) gives them, and when.
//
// The values are checked addresses (the server parses them before storing); "" when unknown.
type ProbeAddrs struct {
	Own, Seen    string
	At           int64
	Public       string
	PublicAt     int64
	Host, HostGW string
	HostAt       int64
}

// SetProbeReportAddrs records the addresses of a probe container's minute report: its own and the
// one Argus saw it from (always), and the public one with when the probe's lookup found it, when the
// report carries one ("" keeps the last one).
func (s *Store) SetProbeReportAddrs(ctx context.Context, proxyName, own, seen, public string, publicAt int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET addr_own=?, addr_seen=?, addr_at=? WHERE proxy_name=?`, own, seen, time.Now().Unix(), proxyName); err != nil {
		return err
	}
	if public == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET addr_public=?, addr_public_at=? WHERE proxy_name=?`, public, publicAt, proxyName)
	return err
}

// SetProbeHostAddr records the host's LAN address and gateway, from a probe VM's OS report or the
// sidecar's check-in ("" ip is ignored: a reporter without them keeps what was there).
func (s *Store) SetProbeHostAddr(ctx context.Context, proxyName, ip, gw string) error {
	if ip == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET addr_host=?, addr_host_gw=?, addr_host_at=? WHERE proxy_name=?`, ip, gw, time.Now().Unix(), proxyName)
	return err
}

// ProbeAddresses returns every probe's addresses, keyed by proxy name.
func (s *Store) ProbeAddresses(ctx context.Context) (map[string]ProbeAddrs, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name,addr_own,addr_seen,addr_at,addr_public,addr_public_at,addr_host,addr_host_gw,addr_host_at FROM probe_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProbeAddrs{}
	for rows.Next() {
		var name string
		var a ProbeAddrs
		if err := rows.Scan(&name, &a.Own, &a.Seen, &a.At, &a.Public, &a.PublicAt, &a.Host, &a.HostGW, &a.HostAt); err != nil {
			return nil, err
		}
		out[name] = a
	}
	return out, rows.Err()
}
