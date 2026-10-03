// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"argus/internal/auth"
	"argus/internal/store"
)

// What each scope may do, and what no token may.
func TestTokenScopes(t *testing.T) {
	cases := []struct {
		scope, method, pattern string
		ok                     bool
	}{
		{"read", "GET", "GET /api/census", true},
		{"read", "POST", "POST /api/events/{id}/ack", false},
		{"ack", "POST", "POST /api/events/{id}/ack", true},
		{"ack", "PUT", "PUT /api/sensors/{key}/note", true},
		{"ack", "POST", "POST /api/maintenance", false},
		{"maint", "POST", "POST /api/maintenance", true},
		{"maint", "DELETE", "DELETE /api/maintenance/{id}", true},
		{"maint", "POST", "POST /api/hosts/{id}/pause", false},
		{"all", "POST", "POST /api/hosts/{id}/pause", true},
		{"all", "POST", "POST /api/me/tokens", false},
		{"all", "DELETE", "DELETE /api/me/tokens/{id}", false},
		{"all", "POST", "POST /api/me/password", false},
		{"all", "POST", "POST /api/me/mfa/disable", false},
		{"all", "GET", "GET /api/probes/{name}/break-glass", false},
		{"all", "DELETE", "DELETE /api/users/{id}/tokens", false},
		{"bogus", "GET", "GET /api/census", false},
	}
	for _, c := range cases {
		if got := tokenRefuses(c.scope, c.method, c.pattern) == ""; got != c.ok {
			t.Errorf("%s %s: allowed=%v, want %v", c.scope, c.pattern, got, c.ok)
		}
	}
}

// A request with a personal token is signed in as its user, held to its scope and logged under the
// token's name; a bad token is refused; a probe's own bearer token passes untouched.
func TestTokenSignIn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	uid, _ := st.CreateUser(ctx, store.User{Email: "ops@example.com", Name: "Ops", PasswordHash: "x", Role: "helpdesk"})
	raw, hash, err := auth.NewAPIToken()
	if err != nil || !strings.HasPrefix(raw, auth.TokenPrefix) {
		t.Fatalf("new token: %q %v", raw, err)
	}
	_, _ = st.CreateAPIToken(ctx, store.APIToken{UserID: uid, Name: "ticketing", Scope: "read"}, hash)

	s := &Server{st: st, mux: http.NewServeMux()}
	var actor string
	s.mux.HandleFunc("GET /api/census", func(w http.ResponseWriter, r *http.Request) {
		_, _, actor = changeActor(r)
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("POST /api/hosts/{id}/pause", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("POST /api/probes/checkin", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.UserFrom(r.Context()); ok {
			t.Error("a probe token signed a user in")
		}
		w.WriteHeader(http.StatusOK)
	})
	h := auth.TokenMiddleware(st, func(*http.Request) string { return "10.0.0.60" })(s.tokenScope(s.mux))
	do := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("GET", "/api/census", raw); c != 200 || actor != "Ops (token ticketing)" {
		t.Fatalf("read: %d %q", c, actor)
	}
	if c := do("POST", "/api/hosts/17/pause", raw); c != http.StatusForbidden {
		t.Fatalf("a read token paused a host: %d", c)
	}
	if c := do("GET", "/api/census", auth.TokenPrefix+"0000"); c != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", c)
	}
	if c := do("POST", "/api/probes/checkin", "probe-token-123"); c != 200 {
		t.Fatalf("probe token: %d", c)
	}
}
