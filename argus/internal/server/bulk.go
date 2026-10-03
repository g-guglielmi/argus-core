// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"argus/internal/auth"
	"argus/internal/provision"
	"argus/internal/store"
)

// Bulk actions (DESIGN section 7d): act on many hosts (the tree's selection) or many sensors (a
// problem list's selection) in one go. Each item is done on its own and reported back with why it
// failed, so one refusal doesn't undo the rest; the change log gets one entry naming them all.

const bulkMax = 2000

type bulkFailure struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error"`
}

type bulkResult struct {
	Done   int           `json:"done"`
	Failed []bulkFailure `json:"failed"`
}

type bulkHostsRequest struct {
	Action          string            `json:"action"` // pause | resume | hide | show | ack | groups | probe | tags | thresholds
	HostIDs         []string          `json:"host_ids"`
	DurationSeconds int64             `json:"duration_seconds"`
	Note            string            `json:"note"`
	GroupIDs        []string          `json:"group_ids"`
	MonitoredBy     int               `json:"monitored_by"`
	ProxyID         string            `json:"proxy_id"`
	Add             []string          `json:"add"`
	Remove          []string          `json:"remove"`
	Macros          map[string]string `json:"macros"`
}

// bulkVerbs is how the change log reads each host action: the verb and its kind.
var bulkVerbs = map[string][2]string{
	"pause": {"Paused", "states"}, "resume": {"Resumed", "states"}, "hide": {"Hid", "states"}, "show": {"Showed again", "states"},
	"ack": {"Acknowledged the problems of", "states"}, "groups": {"Moved", "hosts"}, "probe": {"Moved", "hosts"},
	"tags": {"Changed the tags of", "hosts"}, "thresholds": {"Changed thresholds on", "hosts"},
}

// namesText names what an action touched, the first few and how many more.
func namesText(names []string) string {
	if len(names) <= 4 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
}

// POST /api/bulk/hosts (admin, helpdesk): one action on many hosts.
func (s *Server) handleBulkHosts(w http.ResponseWriter, r *http.Request) {
	var req bulkHostsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	verb, ok := bulkVerbs[req.Action]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	if len(req.HostIDs) == 0 || len(req.HostIDs) > bulkMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("pick between 1 and %d hosts", bulkMax)})
		return
	}
	if !s.zbx.Authenticated() && req.Action != "hide" && req.Action != "show" && req.Action != "tags" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sc := scopeFrom(r)
	caller, _ := auth.UserFrom(r.Context())
	var by int64
	if caller != nil {
		by = caller.ID
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}

	// What the action needs, checked once for all the hosts.
	change := store.Change{Category: verb[1]}
	var groupNames []string
	var tagsAdd, tagsRemove []string
	var proxyLabel string
	switch req.Action {
	case "groups":
		if len(req.GroupIDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick the groups to put them in"})
			return
		}
		gs, err := s.zbx.HostGroups(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
			return
		}
		byID := map[string]string{}
		for _, g := range gs {
			byID[g.GroupID] = g.Name
		}
		for _, id := range req.GroupIDs {
			n := byID[id]
			if n == "" || !sc.coversGroup(n) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
				return
			}
			groupNames = append(groupNames, n)
		}
	case "probe":
		if req.MonitoredBy == 1 && (strings.TrimSpace(req.ProxyID) == "" || strings.TrimSpace(req.ProxyID) == "0") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick a probe"})
			return
		}
		if s.proxyOutOfScope(w, r, req.MonitoredBy, req.ProxyID) {
			return
		}
		pid := "0"
		if req.MonitoredBy == 1 {
			pid = strings.TrimSpace(req.ProxyID)
		}
		proxyLabel = s.proxyNames(ctx)[pid]
	case "tags":
		var msg string
		if tagsAdd, msg = s.knownTags(ctx, req.Add); msg == "" {
			tagsRemove, msg = s.knownTags(ctx, req.Remove)
		}
		if msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		if len(tagsAdd) == 0 && len(tagsRemove) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick tags to add or remove"})
			return
		}
	case "thresholds":
		if len(req.Macros) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "set at least one threshold"})
			return
		}
		// One class's thresholds, so every host must be of it.
		classes, _ := s.st.DeviceClasses(ctx)
		cls := ""
		for _, id := range req.HostIDs {
			if cls == "" {
				cls = classes[id]
			}
			if classes[id] != cls || cls == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "thresholds can be set on hosts of one class at a time"})
				return
			}
		}
		class, ok := provision.ClassByID(cls)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown device class"})
			return
		}
		labels := map[string]provision.ThresholdSpec{}
		for _, tt := range provision.ThresholdsForTemplates(class.HostTemplates()) {
			for _, sp := range tt.Specs {
				labels[sp.Macro] = sp
			}
		}
		for m, v := range req.Macros {
			sp, ok := labels[m]
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown threshold for this class"})
				return
			}
			v = strings.TrimSpace(v)
			if v != "" && !provision.ValidThresholdValue(v) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": sp.Label + " must be a plain number"})
				return
			}
			now := v
			if now == "" {
				now = "default"
			} else if sp.Unit != "" {
				now += " " + sp.Unit
			}
			change.Diff = append(change.Diff, store.ChangeDiff{Field: sp.Label, New: now})
		}
	}

	var census []sensorRow
	if req.Action == "ack" {
		if census, err = s.sensorCensus(ctx); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
			return
		}
	}
	until := untilFrom(req.DurationSeconds)
	note := strings.TrimSpace(req.Note)
	res := bulkResult{Failed: []bulkFailure{}}
	var doneIDs, doneNames []string
	acked := 0
	for _, id := range req.HostIDs {
		h, known := idx[id]
		fail := func(msg string) { res.Failed = append(res.Failed, bulkFailure{ID: id, Name: h.Name, Error: msg}) }
		if !known || !s.hostInScope(ctx, sc, id) {
			fail("host not found")
			continue
		}
		var err error
		switch req.Action {
		case "pause", "resume":
			if err = s.zbx.SetHostEnabled(ctx, id, req.Action == "resume"); err == nil {
				if req.Action == "pause" {
					err = s.st.SetSuppression(ctx, "pause", "host", id, by, "", until)
				} else {
					err = s.st.ClearSuppression(ctx, "pause", "host", id)
				}
			}
		case "hide":
			err = s.st.SetSuppression(ctx, "hide", "host", id, by, note, until)
		case "show":
			err = s.st.ClearSuppression(ctx, "hide", "host", id)
		case "ack":
			for _, row := range census {
				if row.HostID != id || (row.State != "error" && row.State != "warning") {
					continue
				}
				for _, e := range row.EventIDs {
					if err = s.ackEvent(ctx, e, by, note, until); err != nil {
						break
					}
					acked++
				}
				if err != nil {
					break
				}
			}
		case "groups":
			err = s.zbx.SetHostGroups(ctx, id, req.GroupIDs)
		case "probe":
			err = s.zbx.SetHostProxy(ctx, id, req.MonitoredBy, strings.TrimSpace(req.ProxyID))
		case "tags":
			err = s.st.ChangeHostTags(ctx, []string{id}, tagsAdd, tagsRemove)
		case "thresholds":
			err = s.applyClassMacros(ctx, id, req.Macros)
		}
		if err != nil {
			fail(s.errText(r, err))
			continue
		}
		res.Done++
		doneIDs = append(doneIDs, id)
		doneNames = append(doneNames, h.Name)
	}
	if req.Action == "groups" || req.Action == "probe" {
		s.forgetHostIndex()
	}

	if res.Done > 0 {
		change.Action = verb[0] + " " + plural(res.Done, "host", "hosts")
		change.Object = namesText(doneNames)
		change.HostIDs = doneIDs
		switch req.Action {
		case "pause":
			change.Detail = durDetail("until resumed")(map[string]any{"duration_seconds": float64(req.DurationSeconds)})
		case "hide":
			change.Detail = durDetail("until shown again")(map[string]any{"duration_seconds": float64(req.DurationSeconds)})
			change.Reason = note
		case "ack":
			change.Detail = plural(acked, "problem", "problems") + ", " + durDetail("until fixed")(map[string]any{"duration_seconds": float64(req.DurationSeconds)})
			change.Reason = note
		case "groups":
			change.Diff = []store.ChangeDiff{{Field: "Groups", New: strings.Join(groupNames, ", ")}}
		case "probe":
			change.Diff = []store.ChangeDiff{{Field: "Probe", New: proxyLabel}}
		case "tags":
			var parts []string
			if len(tagsAdd) > 0 {
				parts = append(parts, "added "+strings.Join(tagsAdd, ", "))
			}
			if len(tagsRemove) > 0 {
				parts = append(parts, "removed "+strings.Join(tagsRemove, ", "))
			}
			change.Detail = strings.Join(parts, "; ")
		}
		if n := len(res.Failed); n > 0 {
			change.Detail = strings.TrimPrefix(change.Detail+" · "+plural(n, "host", "hosts")+" failed", " · ")
		}
		noteChange(r, change)
	} else {
		skipChange(r)
	}
	writeJSON(w, http.StatusOK, res)
}

type bulkSensorsRequest struct {
	Action          string   `json:"action"` // ack | note | pause | hide
	Keys            []string `json:"keys"`   // sensor keys: an item id, or an Argus-raised row's problem id
	DurationSeconds int64    `json:"duration_seconds"`
	Note            string   `json:"note"` // the acknowledgement's or hide's note, or the sensor note's text
}

// POST /api/bulk/sensors: one action on many sensors (a problem list's selection). Acknowledging is
// open to everyone, like a single acknowledgement; notes, pause and hide to admins and helpdesk.
func (s *Server) handleBulkSensors(w http.ResponseWriter, r *http.Request) {
	var req bulkSensorsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	caller, _ := auth.UserFrom(r.Context())
	if caller == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in"})
		return
	}
	verbs := map[string]string{"ack": "Acknowledged", "note": "Left a note on", "pause": "Paused", "hide": "Hid"}
	verb, ok := verbs[req.Action]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	if t, ok := auth.TokenFrom(r.Context()); ok && t.Scope == "ack" && req.Action != "ack" && req.Action != "note" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this token can only read, acknowledge and add notes"})
		return
	}
	if req.Action != "ack" && caller.Role != "admin" && caller.Role != "helpdesk" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if len(req.Keys) == 0 || len(req.Keys) > bulkMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("pick between 1 and %d sensors", bulkMax)})
		return
	}
	note := strings.TrimSpace(req.Note)
	if req.Action == "note" && (note == "" || utf8.RuneCountInString(note) > sensorNoteMax) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is 1 to 500 characters"})
		return
	}
	if req.Action == "pause" && !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	byKey := map[string]sensorRow{}
	for _, row := range rows {
		if row.ItemID != "" {
			byKey[row.ItemID] = row
		} else {
			for _, e := range row.EventIDs {
				byKey[e] = row
			}
		}
	}
	sc := scopeFrom(r)
	until := untilFrom(req.DurationSeconds)
	now := time.Now().Unix()
	res := bulkResult{Failed: []bulkFailure{}}
	var names, hosts []string
	for _, key := range req.Keys {
		row, known := byKey[key]
		label := row.Label
		if label == "" {
			label = row.Name
		}
		name := row.HostName + " · " + label
		fail := func(msg string) { res.Failed = append(res.Failed, bulkFailure{ID: key, Name: name, Error: msg}) }
		if !known || !s.hostInScope(ctx, sc, row.HostID) {
			fail("sensor not found")
			continue
		}
		var err error
		switch req.Action {
		case "ack":
			if len(row.EventIDs) == 0 {
				fail("it has no open problem")
				continue
			}
			for _, e := range row.EventIDs {
				if err = s.ackEvent(ctx, e, caller.ID, note, until); err != nil {
					break
				}
			}
		case "note":
			if len(row.EventIDs) == 0 {
				fail("it has no open problem: a note stays only until the sensor is OK")
				continue
			}
			_, err = s.st.SetSensorNote(ctx, key, row.HostID, note, caller.ID, userLabel(caller), now)
		case "pause", "hide":
			if row.Synthetic || row.ItemID == "" {
				fail("it isn't a sensor of its own, so it can't be " + map[string]string{"pause": "paused", "hide": "hidden"}[req.Action])
				continue
			}
			if req.Action == "pause" {
				if err = s.zbx.SetItemEnabled(ctx, row.ItemID, false); err == nil {
					err = s.st.SetSuppression(ctx, "pause", "item", row.ItemID, caller.ID, "", until)
				}
			} else {
				err = s.st.SetSuppression(ctx, "hide", "item", row.ItemID, caller.ID, note, until)
			}
		}
		if err != nil {
			fail(s.errText(r, err))
			continue
		}
		res.Done++
		names = append(names, name)
		hosts = append(hosts, row.HostID)
	}
	if res.Done > 0 {
		c := store.Change{Category: "states", Action: verb + " " + plural(res.Done, "sensor", "sensors"), Object: namesText(names), HostIDs: hosts}
		switch req.Action {
		case "ack":
			c.Detail, c.Reason = durDetail("until fixed")(map[string]any{"duration_seconds": float64(req.DurationSeconds)}), note
		case "note":
			c.Detail = note
		case "pause":
			c.Detail = durDetail("until resumed")(map[string]any{"duration_seconds": float64(req.DurationSeconds)})
		case "hide":
			c.Detail, c.Reason = durDetail("until shown again")(map[string]any{"duration_seconds": float64(req.DurationSeconds)}), note
		}
		if n := len(res.Failed); n > 0 {
			c.Detail += " · " + plural(n, "sensor", "sensors") + " failed"
		}
		noteChange(r, c)
	} else {
		skipChange(r)
	}
	writeJSON(w, http.StatusOK, res)
}
