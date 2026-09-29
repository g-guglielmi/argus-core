// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"argus/internal/secret"
)

// A database written under the old ARGUS_SECRET_KEY derivation (SHA-256) must keep opening with
// the same variable after the switch to argon2id: startup detects the legacy key through the
// canary and re-encrypts every stored secret once.
func TestRotateEncryptedSecrets(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	const pass = "correct horse battery staple"

	old := secret.LegacyEnvCipher(pass)
	st.SetCipher(old)
	if err := st.VerifyCipher(ctx); err != nil {
		t.Fatalf("first canary: %v", err)
	}
	if err := st.MetaSetSecret(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	uid := newTestUser(t, st)
	if _, err := st.db.ExecContext(ctx, `UPDATE users SET totp_secret=? WHERE id=?`, old.Encrypt("seed"), uid); err != nil {
		t.Fatal(err)
	}

	neu, _, err := secret.Load(pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(neu)
	if err := st.VerifyCipher(ctx); !errors.Is(err, ErrCipherMismatch) {
		t.Fatalf("new derivation should not open the old canary, got %v", err)
	}
	if err := st.VerifyCipherWith(ctx, old); err != nil {
		t.Fatalf("legacy cipher should open the canary: %v", err)
	}
	n, err := st.RotateEncryptedSecrets(ctx, old)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if n < 3 { // canary, meta secret, TOTP seed
		t.Fatalf("rotated %d rows, want at least 3", n)
	}
	if err := st.VerifyCipher(ctx); err != nil {
		t.Fatalf("canary after rotation: %v", err)
	}
	if v, ok, err := st.MetaGetSecret(ctx, "k"); err != nil || !ok || v != "v" {
		t.Fatalf("meta secret after rotation: %q %v %v", v, ok, err)
	}
	var ts string
	if err := st.db.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id=?`, uid).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if got := neu.Decrypt(ts); got != "seed" {
		t.Fatalf("TOTP seed after rotation: %q", got)
	}
	if _, err := old.TryDecrypt(ts); err == nil {
		t.Fatal("the old key still opens the rotated seed")
	}
}

// A challenge is redeemed by exactly one completion.
func TestConsumeMFAChallengeOnce(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	uid := newTestUser(t, st)
	if err := st.CreateMFAChallenge(ctx, "c1", uid, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ConsumeMFAChallenge(ctx, "c1"); err != nil || !ok {
		t.Fatalf("first consume: %v %v", ok, err)
	}
	if ok, err := st.ConsumeMFAChallenge(ctx, "c1"); err != nil || ok {
		t.Fatalf("second consume should find nothing: %v %v", ok, err)
	}
}
