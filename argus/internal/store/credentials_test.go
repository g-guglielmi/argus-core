// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
	"time"
)

// A replaced password takes every other session, every API token and every reset link with it:
// a stolen cookie turned into a token must not outlive the change.
func TestRevokeUserCredentials(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, User{Email: "a@example.com", PasswordHash: "x", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	for _, id := range []string{"keep", "other"} {
		if err := st.CreateSession(ctx, id, uid, exp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateAPIToken(ctx, APIToken{UserID: uid, Name: "script", Scope: "all"}, "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePasswordReset(ctx, "reset1", uid, exp); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeUserCredentials(ctx, uid, "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionUserTouch(ctx, "keep", 0, 0, time.Now()); err != nil {
		t.Fatalf("the current session went too: %v", err)
	}
	if _, err := st.SessionUserTouch(ctx, "other", 0, 0, time.Now()); err == nil {
		t.Fatal("another session survived")
	}
	if ts, _ := st.APITokens(ctx, uid); len(ts) != 0 {
		t.Fatalf("tokens survived: %d", len(ts))
	}
	if _, err := st.ConsumePasswordReset(ctx, "reset1"); err == nil {
		t.Fatal("a reset link survived")
	}
}

// A TOTP step is claimed once: the second claim of the same (or an older) step loses, so a code
// seen over a shoulder is worthless after its owner used it, whatever the timing.
func TestClaimTOTPStepOnce(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, User{Email: "a@example.com", PasswordHash: "x", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimTOTPStep(ctx, uid, 100); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := st.ClaimTOTPStep(ctx, uid, 100); ok {
		t.Fatal("the same step claimed twice")
	}
	if ok, _ := st.ClaimTOTPStep(ctx, uid, 99); ok {
		t.Fatal("an older step claimed after a newer one")
	}
	if ok, _ := st.ClaimTOTPStep(ctx, uid, 101); !ok {
		t.Fatal("the next step refused")
	}
}

// A WebAuthn ceremony is read and removed in one go, so a captured assertion finishes it once.
func TestConsumeWebAuthnSessionOnce(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SaveWebAuthnSession(ctx, "c1", nil, `{"challenge":"x"}`, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := st.ConsumeWebAuthnSession(ctx, "c1"); err != nil || data != `{"challenge":"x"}` {
		t.Fatalf("first use: %q %v", data, err)
	}
	if _, _, err := st.ConsumeWebAuthnSession(ctx, "c1"); err == nil {
		t.Fatal("the ceremony was finished twice")
	}
	if err := st.SaveWebAuthnSession(ctx, "c2", nil, `{}`, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ConsumeWebAuthnSession(ctx, "c2"); err == nil {
		t.Fatal("an expired ceremony was handed out")
	}
}
