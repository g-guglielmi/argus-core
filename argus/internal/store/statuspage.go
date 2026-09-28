// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// StatusPage is a read-only dashboard opened with a secret link. The token is looked up by its SHA-256
// and also kept encrypted at rest, so an admin can copy the link again.
type StatusPage struct {
	ID           int64
	Name         string
	Sites        []string // host-group names shown; empty = all sites
	AllowCIDRs   string   // comma-separated networks allowed to open it; "" = any
	ExpiresAt    int64    // unix s; 0 = never
	CreatedAt    int64
	CreatedBy    string
	LastViewedAt int64
	HasLink      bool // the link can be copied again (false for pages made before it was kept)
}

// HashStatusToken is how a status-page token is stored and looked up.
func HashStatusToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const statusPageColumns = `id,name,sites,allow_cidrs,expires_at,created_at,created_by,last_viewed_at,token_enc<>''`

func scanStatusPage(row rowScanner) (*StatusPage, error) {
	var p StatusPage
	var sites string
	if err := row.Scan(&p.ID, &p.Name, &sites, &p.AllowCIDRs, &p.ExpiresAt, &p.CreatedAt, &p.CreatedBy, &p.LastViewedAt, &p.HasLink); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Sites = decodeSites(sites)
	return &p, nil
}

// ListStatusPages returns every status page, oldest first.
func (s *Store) ListStatusPages(ctx context.Context) ([]StatusPage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statusPageColumns+` FROM status_pages ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusPage
	for rows.Next() {
		p, err := scanStatusPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetStatusPage returns one status page by id.
func (s *Store) GetStatusPage(ctx context.Context, id int64) (*StatusPage, error) {
	return scanStatusPage(s.db.QueryRowContext(ctx, `SELECT `+statusPageColumns+` FROM status_pages WHERE id=?`, id))
}

// StatusPageByToken finds the page a secret link opens (ErrNotFound for an unknown token).
func (s *Store) StatusPageByToken(ctx context.Context, token string) (*StatusPage, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	return scanStatusPage(s.db.QueryRowContext(ctx, `SELECT `+statusPageColumns+` FROM status_pages WHERE token_hash=?`, HashStatusToken(token)))
}

// CreateStatusPage stores a new page with its token (hashed) and returns its id.
func (s *Store) CreateStatusPage(ctx context.Context, p StatusPage, token string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO status_pages(name,token_hash,token_enc,sites,allow_cidrs,expires_at,created_at,created_by) VALUES(?,?,?,?,?,?,?,?)`,
		p.Name, HashStatusToken(token), s.cipher.Encrypt(token), encodeSites(p.Sites), p.AllowCIDRs, p.ExpiresAt, time.Now().Unix(), p.CreatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateStatusPage changes a page's name, sites, networks and expiry (not its token).
func (s *Store) UpdateStatusPage(ctx context.Context, p StatusPage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE status_pages SET name=?,sites=?,allow_cidrs=?,expires_at=? WHERE id=?`,
		p.Name, encodeSites(p.Sites), p.AllowCIDRs, p.ExpiresAt, p.ID)
	return err
}

// RotateStatusPageToken replaces a page's token: the old link stops working at once.
func (s *Store) RotateStatusPageToken(ctx context.Context, id int64, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE status_pages SET token_hash=?, token_enc=? WHERE id=?`, HashStatusToken(token), s.cipher.Encrypt(token), id)
	return err
}

// StatusPageToken returns a page's current token (decrypted), or ErrNotFound when it isn't kept.
func (s *Store) StatusPageToken(ctx context.Context, id int64) (string, error) {
	var enc string
	err := s.db.QueryRowContext(ctx, `SELECT token_enc FROM status_pages WHERE id=?`, id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enc == "") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.cipher.Decrypt(enc), nil
}

// TouchStatusPage records that the page was just viewed (at most once a minute).
func (s *Store) TouchStatusPage(ctx context.Context, id int64) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `UPDATE status_pages SET last_viewed_at=? WHERE id=? AND last_viewed_at<?`, now, id, now-60)
	return err
}

// DeleteStatusPage removes a page; its link stops working.
func (s *Store) DeleteStatusPage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM status_pages WHERE id=?`, id)
	return err
}
