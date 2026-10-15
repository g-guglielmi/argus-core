// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"argus/internal/auth"
	"argus/internal/mfa"
	"argus/internal/store"
)

const recoveryCodeCount = 10

// GET /api/me/mfa - current MFA status for the signed-in user.
func (s *Server) handleMFAStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	remaining, _ := s.st.CountUnusedRecoveryCodes(r.Context(), u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                  u.TOTPEnabled,
		"recovery_codes_remaining": remaining,
	})
}

// POST /api/me/mfa/setup - begin enrollment: generate a pending secret + QR.
func (s *Server) handleMFASetup(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if u.TOTPEnabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "two-factor is already enabled; disable it first to re-enroll"})
		return
	}
	enr, err := mfa.Generate(u.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.SetTOTPSecret(r.Context(), u.ID, enr.Secret); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, enr)
}

// POST /api/me/mfa/enable - confirm a code against the pending secret and turn MFA on.
func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Reload to get the freshly stored pending secret.
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if u.TOTPEnabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "two-factor is already enabled"})
		return
	}
	if u.TOTPSecret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "start setup first"})
		return
	}
	if !mfa.Validate(req.Code, u.TOTPSecret) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that code isn't valid - check your authenticator and try again"})
		return
	}
	if err := s.st.EnableTOTP(r.Context(), u.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	codes := s.freshRecoveryCodes(w, r, u.ID)
	if codes == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// POST /api/me/mfa/disable - turn MFA off (re-auth with the account password).
func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Turning the second factor off takes the second factor: a password alone (the thing MFA
	// guards against being enough) can't remove it.
	if !s.reauthWith(w, r, u, req.Password, req.Code) {
		return
	}
	if err := s.st.DisableTOTP(r.Context(), caller.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/me/mfa/recovery-codes - regenerate recovery codes (re-auth with password).
func (s *Server) handleMFARegenRecovery(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !u.TOTPEnabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "two-factor is not enabled"})
		return
	}
	if !s.reauth(w, r) {
		return
	}
	codes := s.freshRecoveryCodes(w, r, u.ID)
	if codes == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// POST /api/users/{id}/mfa/reset - admin clears a user's MFA (lockout recovery).
func (s *Server) handleAdminResetMFA(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.st.UserByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err := s.st.DisableTOTP(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// The account is down to one factor now: whoever holds a session on it proves the password again.
	_ = s.st.DeleteUserSessions(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// reauth verifies the caller again from a {"password": "...", "code": "..."} body: the password,
// and the current authenticator code when two-factor is on. Anything that adds a way into the
// account (a passkey, fresh recovery codes, an API token) or takes one away takes both factors,
// or a session plus the password would be enough to leave the second factor behind for good.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request) bool {
	caller, _ := auth.UserFrom(r.Context())
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &req) {
		return false
	}
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return false
	}
	return s.reauthWith(w, r, u, req.Password, req.Code)
}

// reauthWith is reauth for a handler that read the password and code itself. Attempts are
// limited per user: a stolen cookie must not be a place to guess the password or the code at
// argon2 speed.
func (s *Server) reauthWith(w http.ResponseWriter, r *http.Request, u *store.User, password, code string) bool {
	key := "reauth:uid:" + strconv.FormatInt(u.ID, 10)
	if s.rateBlocked(w, key) {
		return false
	}
	if ok, err := auth.VerifyPassword(password, u.PasswordHash); err != nil || !ok {
		s.loginLimiter.Fail(key)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "password is incorrect"})
		return false
	}
	if u.TOTPEnabled && !s.totpOnce(r.Context(), u, code) {
		s.loginLimiter.Fail(key)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "that code isn't valid - check your authenticator and try again"})
		return false
	}
	s.loginLimiter.Reset(key)
	return true
}

// totpOnce accepts a current code once: the time step it matched is claimed (ClaimTOTPStep), so
// the same code is refused for the rest of its window, from this request or a racing one.
func (s *Server) totpOnce(ctx context.Context, u *store.User, code string) bool {
	step, ok := mfa.MatchStep(code, u.TOTPSecret, time.Now())
	if !ok {
		return false
	}
	claimed, err := s.st.ClaimTOTPStep(ctx, u.ID, step)
	return err == nil && claimed
}

// freshRecoveryCodes generates, stores, and returns a new set; writes an error and returns nil on failure.
func (s *Server) freshRecoveryCodes(w http.ResponseWriter, r *http.Request, userID int64) []string {
	plain, hashes, err := mfa.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return nil
	}
	if err := s.st.ReplaceRecoveryCodes(r.Context(), userID, hashes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return nil
	}
	return plain
}
