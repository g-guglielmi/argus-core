// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
	"time"
)

// A token signs in as its user until it expires, its user is disabled or it is revoked; its last use
// is recorded.
func TestAPITokens(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, User{Email: "ops@example.com", PasswordHash: "x", Role: "helpdesk"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAPIToken(ctx, APIToken{UserID: uid, Name: "ticketing", Scope: "ack", Hint: "b7e1"}, "hash1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.CreateAPIToken(ctx, APIToken{UserID: uid, Name: "old", Scope: "read", ExpiresAt: time.Now().Add(-time.Hour).Unix()}, "hash2")
	now := time.Now()
	u, tok, err := st.APITokenUser(ctx, "hash1", "10.0.0.60", now)
	if err != nil || u.ID != uid || tok.Scope != "ack" {
		t.Fatalf("sign in: %v %+v %v", u, tok, err)
	}
	if ts, _ := st.APITokens(ctx, uid); len(ts) != 2 || ts[1].LastIP != "10.0.0.60" && ts[0].LastIP != "10.0.0.60" {
		t.Fatalf("last use: %+v", ts)
	}
	if _, _, err := st.APITokenUser(ctx, "hash2", "", now); err != ErrNotFound {
		t.Fatalf("expired: %v", err)
	}
	if _, _, err := st.APITokenUser(ctx, "nope", "", now); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
	_ = st.SetUserDisabled(ctx, uid, true)
	if _, _, err := st.APITokenUser(ctx, "hash1", "", now); err != ErrNotFound {
		t.Fatalf("disabled user: %v", err)
	}
	if c, _ := st.APITokenCounts(ctx); c[uid] != 2 {
		t.Fatalf("counts: %v", c)
	}
	if err := st.DeleteAPIToken(ctx, id, uid+1); err != ErrNotFound {
		t.Fatalf("someone else's token: %v", err)
	}
	if err := st.DeleteAPIToken(ctx, id, uid); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.DeleteUserAPITokens(ctx, uid); n != 1 {
		t.Fatalf("revoke all: %d", n)
	}
}
