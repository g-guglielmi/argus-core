// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"strings"
)

// DomainCheck is what Argus last read about a domain's registration (server/domains.go): when it
// expires (0 = not known), when it was read and how (rdap | whois), and why the last read didn't work.
type DomainCheck struct {
	Domain    string
	ExpiresAt int64
	CheckedAt int64
	Via       string
	Error     string
}

// DomainChecks returns every domain's last read, by domain.
func (s *Store) DomainChecks(ctx context.Context) (map[string]DomainCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, expires_at, checked_at, via, error FROM domain_checks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DomainCheck{}
	for rows.Next() {
		var c DomainCheck
		if err := rows.Scan(&c.Domain, &c.ExpiresAt, &c.CheckedAt, &c.Via, &c.Error); err != nil {
			return nil, err
		}
		out[c.Domain] = c
	}
	return out, rows.Err()
}

// SaveDomainCheck stores a read. A read that failed keeps the expiry the last good one found.
func (s *Store) SaveDomainCheck(ctx context.Context, c DomainCheck) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO domain_checks(domain, expires_at, checked_at, via, error) VALUES(?,?,?,?,?)
		ON CONFLICT(domain) DO UPDATE SET
			expires_at = CASE WHEN excluded.error = '' THEN excluded.expires_at ELSE domain_checks.expires_at END,
			via = CASE WHEN excluded.error = '' THEN excluded.via ELSE domain_checks.via END,
			checked_at = excluded.checked_at, error = excluded.error`,
		c.Domain, c.ExpiresAt, c.CheckedAt, c.Via, c.Error)
	return err
}

// PruneDomainChecks forgets the domains no host has any more.
func (s *Store) PruneDomainChecks(ctx context.Context, keep map[string]bool) error {
	all, err := s.DomainChecks(ctx)
	if err != nil {
		return err
	}
	for d := range all {
		if !keep[d] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM domain_checks WHERE domain=?`, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// HostDomains returns the domains typed on each host (beside the ones its web checks name).
func (s *Store) HostDomains(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, domain FROM host_domains ORDER BY host_id, domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var h, d string
		if err := rows.Scan(&h, &d); err != nil {
			return nil, err
		}
		out[h] = append(out[h], d)
	}
	return out, rows.Err()
}

// SetHostDomains replaces the domains typed on a host.
func (s *Store) SetHostDomains(ctx context.Context, hostID string, domains []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM host_domains WHERE host_id=?`, hostID); err != nil {
		return err
	}
	for _, d := range domains {
		if d = strings.TrimSpace(d); d != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO host_domains(host_id, domain) VALUES(?,?)`, hostID, d); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
