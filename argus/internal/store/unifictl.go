// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UniFiController is a saved UniFi Network controller the §B sweep enumerates adopted devices
// from. APIKey is plaintext here but encrypted at rest; listing reads leave it empty and set
// HasKey instead - the key is only decrypted at sweep dispatch and adopt-time macro injection.
type UniFiController struct {
	ID          int64
	Name        string
	URL         string
	APIKey      string
	HasKey      bool
	Sites       []string // which probes' sites may receive this controller (empty = every site)
	TLSMode     string   // verify | pin | ignore (see unifi.Options)
	Fingerprint string   // pinned leaf SHA-256 (hex) for "pin"
	CreatedAt   int64
	UpdatedAt   int64
}

// InScope reports whether a probe at site may be handed this controller.
func (c UniFiController) InScope(site string) bool {
	if len(c.Sites) == 0 {
		return true
	}
	for _, s := range c.Sites {
		if s == site {
			return true
		}
	}
	return false
}

// ListUniFiControllers returns every saved controller, name order, keys left empty (HasKey only).
func (s *Store) ListUniFiControllers(ctx context.Context) ([]UniFiController, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, url, (api_key != ''), sites, tls_mode, fingerprint, created_at, updated_at FROM unifi_controllers ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UniFiController
	for rows.Next() {
		var c UniFiController
		var hk int
		var sites string
		if err := rows.Scan(&c.ID, &c.Name, &c.URL, &hk, &sites, &c.TLSMode, &c.Fingerprint, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.HasKey = hk != 0
		c.Sites = decodeSites(sites)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UniFiControllerByID returns one controller with the API key decrypted (dispatch/adopt use only).
func (s *Store) UniFiControllerByID(ctx context.Context, id int64) (*UniFiController, error) {
	var c UniFiController
	var enc, sites string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, url, api_key, sites, tls_mode, fingerprint, created_at, updated_at FROM unifi_controllers WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.URL, &enc, &sites, &c.TLSMode, &c.Fingerprint, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Sites = decodeSites(sites)
	c.APIKey = s.cipher.Decrypt(enc)
	c.HasKey = c.APIKey != ""
	return &c, nil
}

// SaveUniFiController creates (ID 0) or updates a controller. On update an empty APIKey keeps the
// stored one (the settings convention: a blank secret means "unchanged"). Returns the row id.
func (s *Store) SaveUniFiController(ctx context.Context, c UniFiController) (int64, error) {
	now := time.Now().Unix()
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO unifi_controllers(name, url, api_key, sites, tls_mode, fingerprint, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
			c.Name, c.URL, s.cipher.Encrypt(c.APIKey), encodeSites(c.Sites), c.TLSMode, c.Fingerprint, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	set := `name=?, url=?, sites=?, tls_mode=?, fingerprint=?, updated_at=?`
	args := []any{c.Name, c.URL, encodeSites(c.Sites), c.TLSMode, c.Fingerprint, now}
	if c.APIKey != "" {
		set += `, api_key=?`
		args = append(args, s.cipher.Encrypt(c.APIKey))
	}
	args = append(args, c.ID)
	res, err := s.db.ExecContext(ctx, `UPDATE unifi_controllers SET `+set+` WHERE id=?`, args...)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return c.ID, nil
}

// DeleteUniFiController removes a saved controller. Queued sweep jobs referencing it will fail at
// dispatch with a clear error; finished jobs keep their controller_name snapshot.
func (s *Store) DeleteUniFiController(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM unifi_controllers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
