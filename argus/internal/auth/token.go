// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"argus/internal/store"
)

// TokenPrefix starts every personal API token, so one is told apart from the probes' own bearer
// tokens (and is easy to find if it leaks into a file or a log).
const TokenPrefix = "argus_pat_"

// Token is the personal API token a request came with: it acts as its user, narrowed to its scope.
type Token struct {
	ID    int64
	Name  string
	Scope string
}

const tokenKey ctxKey = 1

// NewAPIToken returns a new token (shown once) and the hash that is kept.
func NewAPIToken() (raw, hash string, err error) {
	b := make([]byte, 20)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = TokenPrefix + hex.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// TokenFrom returns the personal API token the request came with, if it came with one.
func TokenFrom(ctx context.Context) (*Token, bool) {
	t, ok := ctx.Value(tokenKey).(*Token)
	return t, ok
}

// WithToken returns ctx carrying t as the request's API token (for tests).
func WithToken(ctx context.Context, t *Token) context.Context {
	return context.WithValue(ctx, tokenKey, t)
}

// TokenMiddleware signs in a request that carries "Authorization: Bearer argus_pat_...": its user
// goes in the context like a session's, and the token next to it. A token that is unknown, expired,
// or whose user can't sign in is refused outright (401), so a script learns at once. Other bearer
// tokens (the probes') pass untouched, and a session cookie wins.
func TokenMiddleware(st *store.Store, clientIP func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") || !strings.HasPrefix(strings.TrimSpace(h[7:]), TokenPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := UserFrom(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			raw := strings.TrimSpace(h[7:])
			u, t, err := st.APITokenUser(r.Context(), HashToken(raw), clientIP(r), time.Now())
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid or expired API token"}`))
				return
			}
			ctx := context.WithValue(r.Context(), userKey, u)
			ctx = context.WithValue(ctx, tokenKey, &Token{ID: t.ID, Name: t.Name, Scope: t.Scope})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
