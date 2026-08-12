package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"argus/internal/auth"
	"argus/internal/store"
)

const minPasswordLen = 8

func validRole(role string) bool {
	switch role {
	case "admin", "helpdesk", "viewer":
		return true
	}
	return false
}

type adminUser struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Surname    string `json:"surname"`
	Role       string `json:"role"`
	MFAEnabled bool   `json:"mfa_enabled"`
	Passkeys   int    `json:"passkeys"`
	Disabled   bool   `json:"disabled"`
}

func toAdminUser(u store.User) adminUser {
	return adminUser{ID: u.ID, Email: u.Email, Name: u.Name, Surname: u.Surname, Role: u.Role, MFAEnabled: u.TOTPEnabled, Disabled: u.Disabled}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		au := toAdminUser(u)
		au.Passkeys, _ = s.st.CountPasskeys(r.Context(), u.ID)
		out = append(out, au)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Surname  string `json:"surname"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || !validRole(req.Role) || len(req.Password) < minPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email, valid role, and a password of at least 8 characters are required"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	id, err := s.st.CreateUser(r.Context(), store.User{
		Email: req.Email, Name: req.Name, Surname: req.Surname, Role: req.Role, PasswordHash: hash,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a user with that email already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusCreated, adminUser{ID: id, Email: req.Email, Name: req.Name, Surname: req.Surname, Role: req.Role})
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Surname string `json:"surname"`
		Role    string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	if !validRole(req.Role) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid role"})
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	// Don't allow demoting the last remaining admin.
	if target.Role == "admin" && req.Role != "admin" {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot demote the last admin"})
			return
		}
	}
	if err := s.st.UpdateUserProfile(r.Context(), id, req.Email, req.Name, req.Surname, req.Role); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a user with that email already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, adminUser{ID: id, Email: req.Email, Name: req.Name, Surname: req.Surname, Role: req.Role, Disabled: target.Disabled})
}

// handleSetUserDisabled suspends or re-enables an account. Guarded so an admin can't disable
// themselves or the last remaining admin.
func (s *Server) handleSetUserDisabled(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	caller, _ := auth.UserFrom(r.Context())
	if req.Disabled && caller != nil && caller.ID == id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you cannot disable your own account"})
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if req.Disabled && target.Role == "admin" {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot disable the last admin"})
			return
		}
	}
	if err := s.st.SetUserDisabled(r.Context(), id, req.Disabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Password) < minPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 characters"})
		return
	}
	if _, err := s.st.UserByID(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.UpdatePassword(r.Context(), id, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	caller, _ := auth.UserFrom(r.Context())
	if caller != nil && caller.ID == id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you cannot delete your own account"})
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if target.Role == "admin" {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete the last admin"})
			return
		}
	}
	if err := s.st.DeleteUser(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new password must be at least 8 characters"})
		return
	}
	// Reload to get the current hash.
	u, err := s.st.UserByID(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if ok, err := auth.VerifyPassword(req.CurrentPassword, u.PasswordHash); err != nil || !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.st.UpdatePassword(r.Context(), caller.ID, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
