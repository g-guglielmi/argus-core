// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// Personal API tokens (DESIGN section 7i): for scripts and other tools. A token acts as its user,
// with their role and sites, never more, narrowed further by its scope; it expires, shows where it
// was last used, is shown once, and every change it makes is in the change log under its name. A
// token can't manage tokens or sign-in (password, two-factor, passkeys) and can't reveal a probe's
// break-glass password: those need a person in the app.

// tokenScopeLabels say what a token's scope lets it do.
var tokenScopeLabels = map[string]string{
	"read":  "read only",
	"ack":   "acknowledge, add notes",
	"maint": "open and close maintenance windows",
	"all":   "everything its user can do",
}

// tokenAckRoutes are the writes an "acknowledge, add notes" token may make (bulk sensors only for
// ack and note, checked in the handler).
var tokenAckRoutes = map[string]bool{
	"POST /api/events/{id}/ack":      true,
	"DELETE /api/events/{id}/ack":    true,
	"PUT /api/sensors/{key}/note":    true,
	"DELETE /api/sensors/{key}/note": true,
	"POST /api/hosts/{id}/journal":   true,
	"POST /api/bulk/sensors":         true,
}

// tokenMaintRoutes are the writes a maintenance token may make.
var tokenMaintRoutes = map[string]bool{
	"POST /api/maintenance":        true,
	"PATCH /api/maintenance/{id}":  true,
	"DELETE /api/maintenance/{id}": true,
}

// tokenRefused is what no token may do, whatever its scope.
func tokenRefused(pattern string) bool {
	if pattern == "GET /api/probes/{name}/break-glass" || pattern == "POST /api/logout" {
		return true
	}
	_, path, _ := strings.Cut(pattern, " ")
	for _, p := range []string{"/api/me/tokens", "/api/me/password", "/api/me/mfa", "/api/me/passkeys", "/api/login", "/api/users/{id}/tokens"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// tokenRefuses says why a token of this scope may not make this request ("" when it may).
func tokenRefuses(scope, method, pattern string) string {
	if tokenRefused(pattern) {
		return "an API token can't do this: sign in to Argus"
	}
	read := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	switch scope {
	case "all":
		return ""
	case "read":
		if read {
			return ""
		}
		return "this token can only read"
	case "ack":
		if read || tokenAckRoutes[pattern] {
			return ""
		}
		return "this token can only read, acknowledge and add notes"
	case "maint":
		if read || tokenMaintRoutes[pattern] {
			return ""
		}
		return "this token can only read and open or close maintenance windows"
	}
	return "this token's scope is unknown"
}

// tokenScope holds a token-signed request to its token's scope.
func (s *Server) tokenScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := auth.TokenFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if msg := tokenRefuses(t.Scope, r.Method, s.routePattern(r)); msg != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": msg})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type tokenView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	Can        string `json:"can"`
	Hint       string `json:"hint"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
	LastIP     string `json:"last_ip,omitempty"`
}

func toTokenView(t store.APIToken) tokenView {
	return tokenView{ID: t.ID, Name: t.Name, Scope: t.Scope, Can: tokenScopeLabels[t.Scope], Hint: t.Hint, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		Expired: t.ExpiresAt > 0 && time.Now().Unix() > t.ExpiresAt, LastUsedAt: t.LastUsedAt, LastIP: t.LastIP}
}

// GET /api/me/tokens: the caller's tokens.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	ts, err := s.st.APITokens(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]tokenView, 0, len(ts))
	for _, t := range ts {
		out = append(out, toTokenView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// tokenExpiryDays are the lifetimes a token can have (0 = never expires).
var tokenExpiryDays = map[int]bool{0: true, 30: true, 90: true, 182: true, 365: true}

const maxTokensPerUser = 25

// POST /api/me/tokens: make a token. The answer carries it, once.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	var req struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Expires int    `json:"expires_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 60 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a name of 1 to 60 characters: what the token is for"})
		return
	}
	if _, ok := tokenScopeLabels[req.Scope]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick what the token can do"})
		return
	}
	if !tokenExpiryDays[req.Expires] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a token expires in 30, 90, 182 or 365 days, or never"})
		return
	}
	ts, err := s.st.APITokens(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if len(ts) >= maxTokensPerUser {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("up to %d tokens: revoke one you no longer use", maxTokensPerUser)})
		return
	}
	for _, t := range ts {
		if strings.EqualFold(t.Name, name) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "you have a token of that name"})
			return
		}
	}
	raw, hash, err := auth.NewAPIToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	t := store.APIToken{UserID: u.ID, Name: name, Scope: req.Scope, Hint: raw[len(raw)-4:]}
	if req.Expires > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(req.Expires) * 24 * time.Hour).Unix()
	}
	id, err := s.st.CreateAPIToken(r.Context(), t, hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	t.ID, t.CreatedAt = id, time.Now().Unix()
	exp := "never expires"
	if t.ExpiresAt > 0 {
		exp = "expires " + time.Unix(t.ExpiresAt, 0).In(s.mgr.Location()).Format("Jan 2, 2006")
	}
	noteChange(r, store.Change{Category: "users", Action: "Created an API token", Object: name, Detail: tokenScopeLabels[req.Scope] + " · role " + u.Role + " · " + exp})
	writeJSON(w, http.StatusOK, map[string]any{"token": raw, "info": toTokenView(t)})
}

// DELETE /api/me/tokens/{id}: revoke one of the caller's tokens.
func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such token"})
		return
	}
	t, err := s.st.APITokenByID(r.Context(), id, u.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such token"})
		return
	}
	if err := s.st.DeleteAPIToken(r.Context(), id, u.ID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such token"})
		return
	}
	noteChange(r, store.Change{Category: "users", Action: "Revoked an API token", Object: t.Name})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/users/{id}/tokens: an admin revokes every token of a user.
func (s *Server) handleRevokeUserTokens(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	n, err := s.st.DeleteUserAPITokens(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if n == 0 {
		skipChange(r)
	}
	noteChange(r, store.Change{Category: "users", Action: "Revoked a user's API tokens", Object: userLabel(target), Detail: plural(int(n), "token", "tokens")})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}
