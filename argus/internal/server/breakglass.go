package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// handleReportBreakGlass receives a probe VM's break-glass console credential (public; authenticated
// by the same long-lived probe token as check-in). The golden image generates a per-VM admin
// password on first boot and reports it here so an operator can reach the VM through the hypervisor
// console if SSH/network is down. Argus stores the password encrypted at rest and reveals it only to
// admins. See deploy/probe-vm.
func (s *Server) handleReportBreakGlass(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	if tok == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing probe token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	proxyName, err := s.st.ProbeNameByToken(ctx, auth.HashToken(tok))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe token"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		user = "argus"
	}
	if strings.TrimSpace(req.Password) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password is required"})
		return
	}
	if err := s.st.SetBreakGlass(ctx, proxyName, user, req.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the credential"})
		return
	}
	s.logger.Info("probe break-glass credential reported", "proxy", proxyName, "user", user)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRevealBreakGlass returns a probe's decrypted break-glass credential (admin only). Kept off the
// general /api/proxies list (which carries only presence) so the password is fetched deliberately, on
// an explicit reveal.
func (s *Server) handleRevealBreakGlass(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	user, secret, at, err := s.st.BreakGlass(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the credential"})
		return
	}
	if secret == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no break-glass credential has been reported for this probe (it's set by the probe VM on first boot)"})
		return
	}
	s.logger.Info("probe break-glass credential revealed", "proxy", name)
	writeJSON(w, http.StatusOK, map[string]any{"username": user, "password": secret, "updated_at": at})
}
