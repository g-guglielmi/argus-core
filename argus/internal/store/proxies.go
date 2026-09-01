package store

import (
	"context"
	"database/sql"
)

// DeleteProxyRecords removes all Argus-side bookkeeping for one proxy (matched by its Zabbix id and
// name): enrollment tokens, check-in/version state, and its per-proxy SNMP default. Called when a
// proxy is deleted through Argus so no orphan rows are left behind.
func (s *Store) DeleteProxyRecords(ctx context.Context, proxyID, proxyName string) error {
	for _, stmt := range []struct {
		q   string
		arg string
	}{
		{`DELETE FROM enroll_tokens WHERE proxy_name=?`, proxyName},
		{`DELETE FROM probe_agents WHERE proxy_name=?`, proxyName},
		{`DELETE FROM snmp_defaults WHERE proxy_id=?`, proxyID},
	} {
		if _, err := s.db.ExecContext(ctx, stmt.q, stmt.arg); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileProxies prunes Argus records left behind by proxies deleted directly in Zabbix (out of
// band). liveNames / liveIDs are the proxies that currently exist in Zabbix. **Pending (unused)
// enrollment tokens are kept** - their proxy doesn't exist yet by design, so they aren't orphans.
// Returns the number of rows pruned.
func (s *Store) ReconcileProxies(ctx context.Context, liveNames, liveIDs map[string]bool) (int, error) {
	pruned := 0
	del := func(q, arg string) error {
		res, err := s.db.ExecContext(ctx, q, arg)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil {
			pruned += int(n)
		}
		return nil
	}

	// probe_agents (keyed by proxy_name): any row without a live proxy is an orphan.
	names, err := s.oneColumn(ctx, `SELECT proxy_name FROM probe_agents`)
	if err != nil {
		return pruned, err
	}
	for _, n := range names {
		if !liveNames[n] {
			if err := del(`DELETE FROM probe_agents WHERE proxy_name=?`, n); err != nil {
				return pruned, err
			}
		}
	}

	// enroll_tokens: only USED tokens whose proxy is gone (a pending token's proxy is expected to be
	// absent until it enrolls, so those stay).
	used, err := s.oneColumn(ctx, `SELECT DISTINCT proxy_name FROM enroll_tokens WHERE used_at IS NOT NULL`)
	if err != nil {
		return pruned, err
	}
	for _, n := range used {
		if !liveNames[n] {
			if err := del(`DELETE FROM enroll_tokens WHERE proxy_name=? AND used_at IS NOT NULL`, n); err != nil {
				return pruned, err
			}
		}
	}

	// snmp_defaults (keyed by proxy_id).
	ids, err := s.oneColumn(ctx, `SELECT proxy_id FROM snmp_defaults`)
	if err != nil {
		return pruned, err
	}
	for _, id := range ids {
		if !liveIDs[id] {
			if err := del(`DELETE FROM snmp_defaults WHERE proxy_id=?`, id); err != nil {
				return pruned, err
			}
		}
	}
	return pruned, nil
}

// oneColumn runs a query that selects a single text column and returns the values.
func (s *Store) oneColumn(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.String)
		}
	}
	return out, rows.Err()
}
