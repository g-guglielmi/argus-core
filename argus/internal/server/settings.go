package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// handleListSettings returns the runtime-editable settings and their current source/lock state.
func (s *Server) handleListSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.List())
}

// handleUpdateSettings validates and applies a batch of setting changes, then returns the
// refreshed list. Env-locked settings are rejected with a clear message.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(req.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no settings to update"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.mgr.Set(ctx, req.Values); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.logger.Info("settings updated", "keys", keysOf(req.Values))
	writeJSON(w, http.StatusOK, s.mgr.List())
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
