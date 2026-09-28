// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"argus/internal/store"
	"argus/internal/unifi"
)

// Saved UniFi controllers for the §B sweep (admin CRUD). The API key is write-only from the
// browser's point of view: listings carry has_key, never the value, and an empty key on an
// update keeps the stored one.

type unifiControllerView struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	HasKey      bool     `json:"has_key"`
	Sites       []string `json:"sites"`       // empty = every site
	TLSMode     string   `json:"tls_mode"`    // verify | pin | ignore
	Fingerprint string   `json:"fingerprint"` // the pinned certificate (hex SHA-256), for "pin"
}

func toControllerView(c store.UniFiController) unifiControllerView {
	sites := c.Sites
	if sites == nil {
		sites = []string{}
	}
	return unifiControllerView{ID: c.ID, Name: c.Name, URL: c.URL, HasKey: c.HasKey, Sites: sites, TLSMode: c.TLSMode, Fingerprint: c.Fingerprint}
}

// controllerOptions is how the core (and, via the hand-out, a probe) talks to a saved controller.
func controllerOptions(c *store.UniFiController) unifi.Options {
	return unifi.Options{TLSMode: c.TLSMode, Fingerprint: c.Fingerprint}
}

// GET /api/discovery/controllers (admin)
func (s *Server) handleListUniFiControllers(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUniFiControllers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list controllers"})
		return
	}
	out := make([]unifiControllerView, 0, len(list))
	for _, c := range list {
		out = append(out, toControllerView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// normalizeControllerURL validates a controller base URL: http(s), a host, no query/fragment;
// trailing slashes are stripped (a path is allowed for reverse-proxied controllers). Returns a
// user-facing error message.
func normalizeControllerURL(raw string) (string, string) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", "the controller URL is required (e.g. https://unifi.example.lan:11443)"
	}
	u, err := url.Parse(c)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "enter the controller's base URL, e.g. https://unifi.example.lan:11443"
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", "the controller URL should be just the base address - no query or fragment"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), ""
}

// POST /api/discovery/controllers (admin) - create (id 0/absent) or update a saved controller.
func (s *Server) handleSaveUniFiController(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          int64    `json:"id"`
		Name        string   `json:"name"`
		URL         string   `json:"url"`
		APIKey      string   `json:"api_key"`     // empty on update = keep the stored key
		Sites       []string `json:"sites"`       // empty = every site
		TLSMode     string   `json:"tls_mode"`    // verify (default) | pin | ignore
		Fingerprint string   `json:"fingerprint"` // with "pin": the certificate the admin confirmed
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a name for the controller is required"})
		return
	}
	u, msg := normalizeControllerURL(req.URL)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if req.ID == 0 && key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an API key is required (UniFi Network -> Settings -> Control Plane -> Integrations)"})
		return
	}
	// The certificate policy. "verify" is checked here and now: the core connects and looks at the
	// certificate. Trusted by the system roots -> saved as verify. Self-signed (the usual console)
	// -> the admin is shown the certificate and asked to pin it (409 with the details; the UI
	// resubmits with tls_mode "pin" + the fingerprint). Unreachable from the core (a controller only
	// a probe's network sees) -> 409 as well, with the choice of pasting a fingerprint or ignoring.
	mode := strings.ToLower(strings.TrimSpace(req.TLSMode))
	fp := unifi.NormalizeFingerprint(req.Fingerprint)
	switch mode {
	case "", unifi.TLSVerify:
		mode = unifi.TLSVerify
		fp = ""
		if strings.HasPrefix(strings.ToLower(u), "https://") {
			ictx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			info, ierr := unifi.Inspect(ictx, u)
			cancel()
			if ierr != nil {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "Argus can't reach this controller to check its certificate: " + ierr.Error(),
					"unreachable": true})
				return
			}
			if !info.Trusted {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "this controller's certificate isn't trusted by the system roots (self-signed); pin it to continue",
					"certificate": info})
				return
			}
		}
	case unifi.TLSPin:
		if len(fp) != 64 || strings.Trim(fp, "0123456789abcdef") != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a pinned certificate is its SHA-256 fingerprint: 64 hex characters"})
			return
		}
	case unifi.TLSIgnore:
		fp = ""
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tls_mode must be verify, pin or ignore"})
		return
	}
	id, err := s.st.SaveUniFiController(r.Context(), store.UniFiController{ID: req.ID, Name: name, URL: u, APIKey: key,
		Sites: cleanSites(req.Sites), TLSMode: mode, Fingerprint: fp})
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "controller not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the controller"})
		return
	}
	s.logger.Info("discovery: unifi controller saved", "id", id, "name", name, "tls", mode, "sites", len(req.Sites))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// DELETE /api/discovery/controllers/{id} (admin)
func (s *Server) handleDeleteUniFiController(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid controller id"})
		return
	}
	if err := s.st.DeleteUniFiController(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "controller not found"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the controller"})
		return
	}
	s.logger.Info("discovery: unifi controller deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
