package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// groupView is one tree group (a Zabbix host group) for the Monitoring group manager: its id, name
// and how many hosts it holds (0 = an empty group, still a valid move target).
type groupView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Hosts int    `json:"hosts"`
}

const maxGroupNameLen = 128

// cleanGroupName trims a submitted group name and reports whether it's acceptable.
func cleanGroupName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && len(s) <= maxGroupNameLen
}

// handleGroups lists every Zabbix host group with its host count - the picker/tree data for group
// management. Read-only, so any signed-in user may call it.
func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	gs, err := s.zbx.HostGroups(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	out := make([]groupView, len(gs))
	for i, g := range gs {
		out[i] = groupView{ID: g.GroupID, Name: g.Name, Hosts: g.Hosts}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateGroup creates a new (empty) host group. Config write - admin/helpdesk only.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name, ok := cleanGroupName(req.Name)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a group name (1-128 characters) is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	id, err := s.zbx.CreateHostGroup(ctx, name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, groupView{ID: id, Name: name})
}

// handleRenameGroup renames a host group. Config write - admin/helpdesk only.
func (s *Server) handleRenameGroup(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	name, ok := cleanGroupName(req.Name)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a group name (1-128 characters) is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := s.zbx.RenameHostGroup(ctx, r.PathValue("id"), name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteGroup deletes an empty host group. It refuses a non-empty group up front (rather than
// letting Zabbix orphan or reject hosts) so the user gets a clear "move hosts out first" message.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	id := r.PathValue("id")
	gs, err := s.zbx.HostGroups(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	for _, g := range gs {
		if g.GroupID == id && g.Hosts > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "move the hosts out of this group before deleting it"})
			return
		}
	}
	if err := s.zbx.DeleteHostGroup(ctx, id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSetHostGroups replaces a host's group membership (the "Edit groups…"/move action). A host
// must keep at least one group. Config write - admin/helpdesk only.
func (s *Server) handleSetHostGroups(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		GroupIDs []string `json:"group_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.GroupIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a host must belong to at least one group"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if err := s.zbx.SetHostGroups(ctx, r.PathValue("id"), req.GroupIDs); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
