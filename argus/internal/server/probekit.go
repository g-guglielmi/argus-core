// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"argus/internal/store"
)

// PUT /api/probes/{name}/kit (admin): {"kit":true} makes the probe a field kit, a probe brought to a
// network for a while: from its next check-in it is handed the field kit core host (Settings, Probes)
// instead of the fleet's, and dials it from its next start. {"kit":false} puts it back.
func (s *Server) handleSetProbeKit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Kit *bool `json:"kit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&body); err != nil || body.Kit == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"kit": true} or {"kit": false}`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.st.SetProbeKit(ctx, name, *body.Kit); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe doesn't check in to Argus, so it can't take a field kit's address"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the field kit switch"})
		return
	}
	s.logger.Info("probe field kit switched", "proxy", name, "kit", *body.Kit)
	writeJSON(w, http.StatusOK, map[string]any{"kit": *body.Kit, "core_host": s.probeCoreHostFor(ctx, name)})
}
