// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

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
	if sc := scopeFrom(r); !sc.all { // per-site visibility (scope.go)
		vis, err := s.visibleHosts(ctx, sc)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
		sets = scopedOrderSets(sc, vis, sets)
	}
	writeJSON(w, http.StatusOK, sets)
}

// scopedOrderSets keeps the orderings a scoped user's tree uses, naming only their hosts and groups.
// A "sibling" set mixes both: a host id is numeric, a group path isn't.
func scopedOrderSets(sc siteScope, vis map[string]bool, sets []store.OrderSet) []store.OrderSet {
	out := make([]store.OrderSet, 0, len(sets))
	for _, set := range sets {
		if set.Scope != "" && !sc.showsGroup(set.Scope) {
			continue
		}
		items := make([]string, 0, len(set.Items))
		for _, it := range set.Items {
			if isNumericID(it) {
				if vis[it] {
					items = append(items, it)
				}
			} else if sc.showsGroup(it) {
				items = append(items, it)
			}
		}
		out = append(out, store.OrderSet{Scope: set.Scope, Kind: set.Kind, Items: items})
	}
	return out
}

func isNumericID(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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
	if req.Kind != "group" && req.Kind != "host" && req.Kind != "sibling" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `kind must be "group", "host" or "sibling"`})
		return
	}
	if sc := scopeFrom(r); !sc.all && (req.Scope == "" || !sc.coversGroup(req.Scope)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can only reorder inside your sites"})
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

// handleHiddenGroups returns the group paths hidden from the monitoring tree. Read-only.
func (s *Server) handleHiddenGroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	paths, err := s.st.HiddenGroups(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read hidden groups"})
		return
	}
	if paths == nil {
		paths = []string{}
	}
	if sc := scopeFrom(r); !sc.all {
		kept := paths[:0]
		for _, p := range paths {
			if sc.showsGroup(p) {
				kept = append(kept, p)
			}
		}
		paths = kept
	}
	writeJSON(w, http.StatusOK, paths)
}

// handleSetHiddenGroup hides or unhides one group path in the monitoring tree. Admin only: unhiding is
// only reachable in advanced mode (an admin-only preference), so hiding is admin-gated too, keeping the
// two operations together. The group is untouched in Zabbix - this only affects the Argus tree.
func (s *Server) handleSetHiddenGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Hidden bool   `json:"hidden"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a group path is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := s.st.SetGroupHidden(ctx, req.Path, req.Hidden); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update group visibility"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
