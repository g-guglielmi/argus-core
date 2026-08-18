package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ProbeAgent is a probe's fleet-update state: the version it last reported, whether its
// self-updater is enabled, and when it last checked in. Keyed by proxy name.
type ProbeAgent struct {
	ProxyName   string
	Version     string
	SelfUpdate  bool
	LastCheckin int64 // unix seconds, 0 if never
}

// UpsertProbeCredential issues (or rotates) a probe's long-lived check-in credential at
// enrollment. A re-enrollment resets the reported state so stale version data doesn't linger.
func (s *Store) UpsertProbeCredential(ctx context.Context, proxyName, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO probe_agents(proxy_name,token_hash,created_at,version,selfupdate,last_checkin)
		 VALUES(?,?,?,'',0,0)
		 ON CONFLICT(proxy_name) DO UPDATE SET token_hash=excluded.token_hash, created_at=excluded.created_at,
		   version='', selfupdate=0, last_checkin=0`,
		proxyName, tokenHash, time.Now().Unix())
	return err
}

// ProbeNameByToken returns the proxy name for a valid check-in credential (by token hash).
func (s *Store) ProbeNameByToken(ctx context.Context, tokenHash string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT proxy_name FROM probe_agents WHERE token_hash=?`, tokenHash).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// RecordProbeCheckin refreshes a probe's reported version, self-updater flag, and check-in time.
func (s *Store) RecordProbeCheckin(ctx context.Context, proxyName, version string, selfUpdate bool) error {
	su := 0
	if selfUpdate {
		su = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE probe_agents SET version=?, selfupdate=?, last_checkin=? WHERE proxy_name=?`,
		version, su, time.Now().Unix(), proxyName)
	return err
}

// ProbeAgents returns every probe's fleet-update state, keyed by proxy name (for the fleet view).
func (s *Store) ProbeAgents(ctx context.Context) (map[string]ProbeAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name,version,selfupdate,last_checkin FROM probe_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ProbeAgent)
	for rows.Next() {
		var a ProbeAgent
		var su int
		if err := rows.Scan(&a.ProxyName, &a.Version, &su, &a.LastCheckin); err != nil {
			return nil, err
		}
		a.SelfUpdate = su != 0
		out[a.ProxyName] = a
	}
	return out, rows.Err()
}

// --- probe fleet target version (app_meta) ---

const probeTargetKey = "probe_target_version"

// ProbeTargetVersion returns the fleet's target probe version ("latest" by default).
func (s *Store) ProbeTargetVersion(ctx context.Context) (string, error) {
	v, ok, err := s.MetaGet(ctx, probeTargetKey)
	if err != nil {
		return "", err
	}
	if !ok || v == "" {
		return "latest", nil
	}
	return v, nil
}

// SetProbeTargetVersion stores the fleet's target probe version.
func (s *Store) SetProbeTargetVersion(ctx context.Context, v string) error {
	return s.MetaSet(ctx, probeTargetKey, v)
}
