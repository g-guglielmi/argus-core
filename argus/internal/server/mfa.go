package server

import (
	"net/http"

	"argus/internal/auth"
	"argus/internal/mfa"
)

const recoveryCodeCount = 10

// GET /api/me/mfa - current MFA status for the signed-in user.
func (s *Server) handleMFAStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	remaining, _ := s.st.CountUnusedRecoveryCodes(r.Context(), u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                   u.TOTPEnabled,
		"recovery_codes_remaining":  remaining,
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
	if !s.reauth(w, r) {
		return
	}
	caller, _ := auth.UserFrom(r.Context())
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// reauth verifies the caller's password from a {"password": "..."} body.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request) bool {
	caller, _ := auth.UserFrom(r.Context())
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return false
	}
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return false
	}
	if ok, err := auth.VerifyPassword(req.Password, u.PasswordHash); err != nil || !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "password is incorrect"})
		return false
	}
	return true
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
