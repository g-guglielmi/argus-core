// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"fmt"
	"net/http"
)

// m365HealthView is how reading the tenant's Microsoft 365 service health has gone, for Settings.
type m365HealthView struct {
	Configured bool   `json:"configured"`
	ReadAt     int64  `json:"read_at,omitempty"`
	Open       int    `json:"open"`
	Error      string `json:"error,omitempty"`
	ErrorAt    int64  `json:"error_at,omitempty"`
}

func m365HealthNow(configured bool) m365HealthView {
	m365Health.Lock()
	defer m365Health.Unlock()
	return m365HealthView{Configured: configured, ReadAt: m365Health.ReadAt, Open: m365Health.Open, Error: m365Health.Error, ErrorAt: m365Health.ErrorAt}
}

// GET /api/settings/m365health - how reading the tenant's service health has gone (admin).
func (s *Server) handleM365HealthStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, m365HealthNow(s.mgr.M365().Configured()))
}

// POST /api/settings/m365health/test - reads the tenant's service health now and says what it found
// (admin).
func (s *Server) handleM365HealthTest(w http.ResponseWriter, r *http.Request) {
	if !s.mgr.M365().Configured() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set the tenant ID, the application ID and the client secret first"})
		return
	}
	issues, err := s.readTenantHealth(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	n := countIncidents(issues)
	msg := "Read: no incident open in your tenant."
	if n > 0 {
		msg = fmt.Sprintf("Read: %d incident%s open in your tenant.", n, map[bool]string{true: "", false: "s"}[n == 1])
	}
	if adv := len(issues) - n; adv > 0 {
		msg += fmt.Sprintf(" %d advisor%s too, not shown.", adv, map[bool]string{true: "y", false: "ies"}[adv == 1])
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg, "open": n})
}
