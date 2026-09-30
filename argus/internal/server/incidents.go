// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Incident history: what went wrong and when, per host and across the fleet. Zabbix keeps every
// problem event with the recovery that closed it; the problems Argus raises itself (a sensor that
// stopped collecting, an interface that stopped answering) exist only while they are open, so the
// notifier logs those in argus_incidents. Acknowledgements are Argus's own, and a collector flag's
// incident carries the reason the collector gave when it started (reasons.go).

const (
	incidentsDefaultDays = 7
	incidentsMaxDays     = 90
	incidentsMaxRows     = 500
	incidentReasonLookup = 50 // incidents per request whose reason is read from the reason item's history
	argusIncidentKeep    = 120 * 24 * time.Hour
)

type incidentView struct {
	EventID  string `json:"event_id"`
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Site     string `json:"site,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
	Sensor   string `json:"sensor,omitempty"` // the sensor it was on
	Name     string `json:"name"`             // what happened (the trigger's or Argus's problem name)
	Severity int    `json:"severity"`
	Start    int64  `json:"start"`
	End      int64  `json:"end,omitempty"` // 0 = still open
	AckBy    string `json:"ack_by,omitempty"`
	AckNote  string `json:"ack_note,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Argus    bool   `json:"argus,omitempty"` // raised by Argus, not a Zabbix trigger
}

// collectIncidents gathers the incidents that started since from (or are still open), newest
// first, for the given hosts (all when hostIDs is empty), at most limit. Warnings and above only.
// itemIDs narrows it to those sensors (a drilled-down sensor, or all channels of a group): Zabbix is
// asked for their triggers' events only, so a busy host's other incidents can't crowd them out.
func (s *Server) collectIncidents(ctx context.Context, hostIDs, itemIDs []string, from int64, limit int) ([]incidentView, error) {
	var tids []string
	if len(itemIDs) > 0 {
		trigs, err := s.zbx.ItemTriggers(ctx, itemIDs)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range itemIDs {
			for _, t := range trigs[id] {
				if !seen[t.TriggerID] {
					seen[t.TriggerID] = true
					tids = append(tids, t.TriggerID)
				}
			}
		}
	}
	var evs []zabbix.Event
	if len(itemIDs) == 0 || len(tids) > 0 { // sensors without a trigger have no Zabbix incidents
		var err error
		if evs, err = s.zbx.ProblemEvents(ctx, hostIDs, tids, from, limit); err != nil {
			return nil, err
		}
	}
	wantItem := map[string]bool{}
	for _, id := range itemIDs {
		wantItem[id] = true
	}
	var out []incidentView
	var rIDs, evTids []string
	for _, e := range evs {
		if atoi(e.Severity) < 2 || len(e.Hosts) == 0 {
			continue
		}
		if e.REventID != "" && e.REventID != "0" {
			rIDs = append(rIDs, e.REventID)
		}
		evTids = append(evTids, e.ObjectID)
	}
	ends, _ := s.zbx.EventClocks(ctx, rIDs)
	trigItems, _ := s.zbx.TriggerItems(ctx, evTids)
	var labelIDs []string // the sensors to name
	for _, e := range evs {
		if ids := trigItems[e.ObjectID]; len(ids) > 0 {
			labelIDs = append(labelIDs, ids...)
		}
	}
	for _, e := range evs {
		if atoi(e.Severity) < 2 || len(e.Hosts) == 0 {
			continue
		}
		v := incidentView{EventID: e.EventID, HostID: e.Hosts[0].HostID, HostName: e.Hosts[0].Name, Name: e.Name,
			Severity: atoi(e.Severity), Start: atoi64(e.Clock), End: ends[e.REventID]}
		if ids := trigItems[e.ObjectID]; len(ids) > 0 {
			v.ItemID = ids[0]
			for _, id := range ids { // a multi-sensor trigger belongs to the channel asked for
				if wantItem[id] {
					v.ItemID = id
					break
				}
			}
		}
		out = append(out, v)
	}

	argusRows, _ := s.st.ArgusIncidents(ctx, hostIDs, from, limit)
	for _, a := range argusRows {
		if len(wantItem) > 0 && !wantItem[a.ItemID] {
			continue
		}
		out = append(out, incidentView{EventID: a.EventID, HostID: a.HostID, HostName: a.HostName, ItemID: a.ItemID,
			Name: a.Name, Severity: a.Severity, Start: a.StartedAt, End: a.EndedAt, Reason: a.Reason, Argus: true})
		if a.ItemID != "" {
			labelIDs = append(labelIDs, a.ItemID)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start > out[j].Start
		}
		return out[i].EventID > out[j].EventID
	})
	if len(out) > limit {
		out = out[:limit]
	}

	// The sensor each one was on, and for a collector flag the reason it gave then.
	items, _ := s.zbx.ItemsByIDs(ctx, labelIDs)
	lookups := 0
	for i := range out {
		it, ok := items[out[i].ItemID]
		if !ok {
			continue
		}
		out[i].Sensor = sensorLabel(it.Key, it.Name)
		if out[i].Reason == "" && reasonKeyFor(it.Key) != "" && lookups < incidentReasonLookup {
			lookups++
			out[i].Reason = s.reasonAt(ctx, out[i].HostID, reasonKeyFor(it.Key), out[i].Start)
		}
	}

	// Sites, and who acknowledged.
	siteOf := map[string]string{}
	if hosts, err := s.zbx.Hosts(ctx); err == nil {
		for _, h := range hosts {
			groups := make([]string, 0, len(h.Groups))
			for _, g := range h.Groups {
				groups = append(groups, g.Name)
			}
			siteOf[h.HostID] = primarySite(groups)
		}
	}
	ids := make([]string, 0, len(out))
	for _, v := range out {
		ids = append(ids, v.EventID)
	}
	acks, _ := s.st.AckRecords(ctx, ids)
	names := map[int64]string{}
	if users, err := s.st.ListUsers(ctx); err == nil {
		for _, u := range users {
			if n := strings.TrimSpace(u.Name + " " + u.Surname); n != "" {
				names[u.ID] = n
			} else {
				names[u.ID] = u.Email
			}
		}
	}
	for i := range out {
		out[i].Site = siteOf[out[i].HostID]
		if a, ok := acks[out[i].EventID]; ok {
			out[i].AckNote = a.Note
			if a.By == 0 {
				out[i].AckBy = "the link in an alert"
			} else {
				out[i].AckBy = names[a.By]
			}
		}
	}
	return out, nil
}

// reasonAt reads what a collector's reason item said when an incident started: its first non-empty
// value from a couple of minutes before to ten minutes after (the flag and its reason come from the
// same poll, but the item only stores a change).
func (s *Server) reasonAt(ctx context.Context, hostID, reasonKey string, start int64) string {
	id, err := s.zbx.HostItemID(ctx, hostID, reasonKey)
	if err != nil || id == "" {
		return ""
	}
	pts, err := s.zbx.TextHistory(ctx, id, start-3600, start+600)
	if err != nil {
		return ""
	}
	// The value in force at the start is the last one at or before it; failing that, the first after.
	best := ""
	for _, p := range pts {
		if atoi64(p.Clock) <= start+120 {
			best = strings.TrimSpace(p.Value)
		} else if best == "" {
			best = strings.TrimSpace(p.Value)
		}
	}
	return itemError(best)
}

// incidentWindow reads ?days= (default, capped) and returns the start of the window.
func incidentWindow(r *http.Request, def int) int64 {
	days := def
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && n > 0 {
		days = n
	}
	if days > incidentsMaxDays {
		days = incidentsMaxDays
	}
	return time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
}

// handleIncidents serves GET /api/incidents?days=N: the incident feed across all hosts.
func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	s.serveIncidents(w, r, nil, incidentsDefaultDays)
}

// handleHostIncidents serves GET /api/hosts/{id}/incidents?days=N (default 30), and with
// &items=a,b,c only those sensors' incidents (a drilled-down sensor).
func (s *Server) handleHostIncidents(w http.ResponseWriter, r *http.Request) {
	s.serveIncidents(w, r, []string{r.PathValue("id")}, 30)
}

// incidentItems reads ?items= (comma-separated sensor ids, at most 50).
func incidentItems(r *http.Request) []string {
	var out []string
	for _, id := range strings.Split(r.URL.Query().Get("items"), ",") {
		if id = strings.TrimSpace(id); id != "" && len(out) < 50 {
			if _, err := strconv.ParseUint(id, 10, 64); err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

func (s *Server) serveIncidents(w http.ResponseWriter, r *http.Request, hostIDs []string, defDays int) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	from := incidentWindow(r, defDays)
	// Per-site visibility (scope.go): the fleet feed of a scoped user asks Zabbix for their hosts
	// only, so other sites' incidents can't crowd theirs out of the row limit either.
	if sc := scopeFrom(r); hostIDs == nil && !sc.all {
		vis, err := s.visibleHosts(ctx, sc)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
			return
		}
		if len(vis) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"from": from, "incidents": []incidentView{}})
			return
		}
		hostIDs = visibleHostIDs(vis)
	}
	out, err := s.collectIncidents(ctx, hostIDs, incidentItems(r), from, incidentsMaxRows)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if out == nil {
		out = []incidentView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "incidents": out})
}

var argusIncidentPrune struct {
	mu   sync.Mutex
	last time.Time
}

// recordArgusIncidents logs the Argus-raised problems open right now (notifier tick). Only a complete
// set may close incidents: a failed lookup would otherwise end every one of them.
func recordArgusIncidents(ctx context.Context, st *store.Store, synth synthSet) {
	if !synth.complete {
		return
	}
	open := make([]store.OpenArgusIncident, 0, len(synth.problems))
	for _, p := range synth.problems {
		t := synth.targets[p.ObjectID]
		if len(t.Hosts) == 0 {
			continue
		}
		o := store.OpenArgusIncident{EventID: p.EventID, HostID: t.Hosts[0].HostID, HostName: t.Hosts[0].Name,
			Name: p.Name, Severity: atoi(p.Severity), StartedAt: atoi64(p.Clock),
			Reason: strings.TrimPrefix(synth.readings[p.EventID], "Not supported: ")}
		if len(t.Items) > 0 {
			o.ItemID = t.Items[0].ItemID
		}
		open = append(open, o)
	}
	now := time.Now()
	_ = st.SyncArgusIncidents(ctx, open, now.Unix())
	argusIncidentPrune.mu.Lock()
	due := now.Sub(argusIncidentPrune.last) > 24*time.Hour
	if due {
		argusIncidentPrune.last = now
	}
	argusIncidentPrune.mu.Unlock()
	if due {
		_ = st.PruneArgusIncidents(ctx, now.Add(-argusIncidentKeep).Unix())
	}
}
