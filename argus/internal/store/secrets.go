// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"fmt"

	"argus/internal/secret"
)

// cipherCanaryKey holds a known plaintext encrypted with the key in use, so a start with a
// different key (a lost secret.key, a changed ARGUS_SECRET_KEY, a switch between the two) is
// caught before the app runs with secrets it can't read.
const cipherCanaryKey = "cipher_canary"

const cipherCanaryPlain = "argus"

// ErrCipherMismatch says the database was encrypted with a different key than the one loaded.
var ErrCipherMismatch = errors.New("the stored secrets were encrypted with a different key")

// VerifyCipher checks the loaded key against the canary, writing the canary on first use.
func (s *Store) VerifyCipher(ctx context.Context) error {
	if !s.cipher.Enabled() {
		return nil
	}
	raw, ok, err := s.MetaGet(ctx, cipherCanaryKey)
	if err != nil {
		return err
	}
	if !ok || raw == "" {
		return s.MetaSet(ctx, cipherCanaryKey, s.cipher.Encrypt(cipherCanaryPlain))
	}
	return s.VerifyCipherWith(ctx, s.cipher)
}

// VerifyCipherWith reports whether the given cipher opens the canary: ErrCipherMismatch when it
// doesn't, or when there is no canary yet.
func (s *Store) VerifyCipherWith(ctx context.Context, c *secret.Cipher) error {
	raw, ok, err := s.MetaGet(ctx, cipherCanaryKey)
	if err != nil {
		return err
	}
	if !ok || raw == "" {
		return ErrCipherMismatch
	}
	if pt, err := c.TryDecrypt(raw); err != nil || pt != cipherCanaryPlain {
		return ErrCipherMismatch
	}
	return nil
}

// encryptedColumns lists every column that may hold a marked ciphertext (for key rotation).
var encryptedColumns = [][2]string{
	{"users", "totp_secret"},
	{"notify_channels", "config"},
	{"user_notify_channels", "config"},
	{"snmp_defaults", "community"},
	{"snmp_defaults", "auth_pass"},
	{"snmp_defaults", "priv_pass"},
	{"discovery_jobs", "snmp_community"},
	{"unifi_controllers", "api_key"},
	{"probe_agents", "bg_secret"},
	{"status_pages", "token_enc"},
	{"push_sensors", "token_enc"},
	{"app_meta", "value"},
}

// RotateEncryptedSecrets re-encrypts every stored secret from old to the current cipher in one
// transaction and leaves a fresh canary, so a database keeps opening with the same
// ARGUS_SECRET_KEY after the key derivation changed. It returns the rows rewritten. A row old
// can't open is left as it is (startup verified old against the canary, so that is a corrupt row).
func (s *Store) RotateEncryptedSecrets(ctx context.Context, old *secret.Cipher) (int, error) {
	if !s.cipher.Enabled() || old == nil {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, tc := range encryptedColumns {
		table, col := tc[0], tc[1]
		type upd struct {
			id int64
			v  string
		}
		var todo []upd
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT rowid,%s FROM %s WHERE %s LIKE ?`, col, table, col), marker()+"%")
		if err != nil {
			return 0, fmt.Errorf("%s.%s: %w", table, col, err)
		}
		for rows.Next() {
			var id int64
			var v string
			if err := rows.Scan(&id, &v); err != nil {
				rows.Close()
				return 0, err
			}
			pt, err := old.TryDecrypt(v)
			if err != nil {
				continue
			}
			todo = append(todo, upd{id, s.cipher.Encrypt(pt)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
		for _, u := range todo {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET %s=? WHERE rowid=?`, table, col), u.v, u.id); err != nil {
				return 0, fmt.Errorf("%s.%s: %w", table, col, err)
			}
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, s.MetaSet(ctx, cipherCanaryKey, s.cipher.Encrypt(cipherCanaryPlain))
}

// ResetEncryptedSecrets drops every value encrypted with a key that is no longer available, so
// the app can start again with the current one: channel credentials, SNMP and UniFi credentials,
// break-glass passwords, status-page link copies, the alert signing key and encrypted settings are
// cleared (to be entered again), and two-factor is switched off for the users whose TOTP seed is
// unreadable (they set it up again; their recovery codes go with it). It returns how many rows
// were touched. Unencrypted rows are left alone.
func (s *Store) ResetEncryptedSecrets(ctx context.Context) (int, error) {
	if !s.cipher.Enabled() {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	run := func(q string, args ...any) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
		if k, err := res.RowsAffected(); err == nil {
			n += int(k)
		}
		return nil
	}
	enc := marker() + "%"
	steps := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM recovery_codes WHERE user_id IN (SELECT id FROM users WHERE totp_secret LIKE ?)`, []any{enc}},
		{`UPDATE users SET totp_secret='', totp_enabled=0 WHERE totp_secret LIKE ?`, []any{enc}},
		{`UPDATE notify_channels SET config='{}' WHERE config LIKE ?`, []any{enc}},
		{`UPDATE user_notify_channels SET config='{}' WHERE config LIKE ?`, []any{enc}},
		{`UPDATE snmp_defaults SET community='' WHERE community LIKE ?`, []any{enc}},
		{`UPDATE snmp_defaults SET auth_pass='' WHERE auth_pass LIKE ?`, []any{enc}},
		{`UPDATE snmp_defaults SET priv_pass='' WHERE priv_pass LIKE ?`, []any{enc}},
		{`UPDATE discovery_jobs SET snmp_community='' WHERE snmp_community LIKE ?`, []any{enc}},
		{`UPDATE unifi_controllers SET api_key='' WHERE api_key LIKE ?`, []any{enc}},
		{`UPDATE probe_agents SET bg_secret='' WHERE bg_secret LIKE ?`, []any{enc}},
		{`UPDATE status_pages SET token_enc='' WHERE token_enc LIKE ?`, []any{enc}},
		{`UPDATE push_sensors SET token_enc='' WHERE token_enc LIKE ?`, []any{enc}},
		{`DELETE FROM app_meta WHERE value LIKE ?`, []any{enc}},
	}
	for _, st := range steps {
		if err := run(st.q, st.args...); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// A fresh canary for the key now in use.
	return n, s.MetaSet(ctx, cipherCanaryKey, s.cipher.Encrypt(cipherCanaryPlain))
}

// marker is the ciphertext prefix, as a LIKE pattern head.
func marker() string { return secret.Marker }
