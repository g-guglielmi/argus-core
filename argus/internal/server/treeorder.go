package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"argus/internal/store"
)

// handleTreeOrder returns every saved manual sibling ordering for the monitoring tree. Argus-local
// (Zabbix has no group/host order), so it needs no Zabbix token; read-only, any signed-in user.
func (s *Server) handleTreeOrder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	sets, err := s.st.TreeOrder(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the tree order"})
		return
	}
	if sets == nil {
		sets = []store.OrderSet{}
	}
	writeJSON(w, http.StatusOK, sets)
}

// handleSetTreeOrder replaces the manual order of one sibling set - a parent's child groups or its
// hosts. Config write, admin/helpdesk only. An empty items list reverts the set to alphabetical.
func (s *Server) handleSetTreeOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope string   `json:"scope"`
		Kind  string   `json:"kind"`
		Items []string `json:"items"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Kind != "group" && req.Kind != "host" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `kind must be "group" or "host"`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := s.st.SetTreeOrder(ctx, req.Scope, req.Kind, req.Items); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the tree order"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
