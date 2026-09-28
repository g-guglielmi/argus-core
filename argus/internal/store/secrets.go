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
	if pt, err := s.cipher.TryDecrypt(raw); err != nil || pt != cipherCanaryPlain {
		return ErrCipherMismatch
	}
	return nil
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
