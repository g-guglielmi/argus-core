// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"argus/internal/store"
)

// CookieName is the session cookie; over HTTPS it carries the __Host- prefix, which the browser
// only honours when the cookie is Secure, has Path=/ and no Domain - so a plain-HTTP page or a
// sibling subdomain can't set one that Argus would then read.
const (
	CookieName       = "argus_session"
	SecureCookieName = "__Host-" + CookieName
)

// SessionCookie returns the raw session token carried by a request, checking the secure name
// first, and "" when there is none.
func SessionCookie(r *http.Request) string {
	for _, name := range []string{SecureCookieName, CookieName} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

type ctxKey int

const userKey ctxKey = 0

// NewSessionToken returns (raw, id). `raw` goes in the cookie; `id` (its SHA-256) is what we
// persist, so a database leak never exposes usable session tokens.
func NewSessionToken() (raw, id string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashToken(raw), nil
}

// HashToken maps a raw cookie token to its stored session id.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Middleware loads the session user into the request context when a valid cookie is present.
// idle and maxLife return the currently-configured idle timeout and absolute max lifetime (0 =
// disabled); both are read per request so an admin's change in Settings takes effect immediately.
func Middleware(st *store.Store, idle, maxLife func() time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if raw := SessionCookie(r); raw != "" {
				if u, err := st.SessionUserTouch(r.Context(), HashToken(raw), idle(), maxLife(), time.Now()); err == nil {
					r = r.WithContext(context.WithValue(r.Context(), userKey, u))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserFrom returns the authenticated user from the context, if any.
func UserFrom(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(userKey).(*store.User)
	return u, ok
}

// RequireAuth rejects requests with no authenticated user (401 JSON).
func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// RequireRole rejects requests unless the authenticated user has the given role.
func RequireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return RequireRoles(next, role)
}

// RequireRoles rejects requests unless the authenticated user's role is in the allowed set.
func RequireRoles(next http.HandlerFunc, roles ...string) http.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !allowed[u.Role] {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func SetSessionCookie(w http.ResponseWriter, raw string, secure bool, ttl time.Duration) {
	name := CookieName
	if secure {
		name = SecureCookieName
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// ClearSessionCookie expires both names, so a sign-out works whichever one the browser holds.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SecureCookieName, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}
