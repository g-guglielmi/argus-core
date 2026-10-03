// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Sensor notes: a line someone leaves on a sensor in trouble ("ISP ticket 4471 open, technician on
// site at 14:00"), for whoever looks at it next. It shows with the sensor (the lists, the host page,
// the status pages), goes out with the sensor's alerts, reminders and RESOLVED, and clears itself
// once the sensor is OK again; the incident history keeps it. It belongs to the sensor, not to a
// Zabbix event, so it survives the warning-to-error handover and works on Argus-raised problems.

// sensorNoteMax is how long a note may be, in characters.
const sensorNoteMax = 500

// sensorNoteKeyShape is a sensor key: an item id, or an Argus-raised problem id.
var sensorNoteKeyShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// sensorNoteView is a note as the app and the API show it.
type sensorNoteView struct {
	Text string `json:"text"`
	By   string `json:"by,omitempty"`
	At   int64  `json:"at"` // when it was last written
}

func noteViewOf(n store.SensorNote) *sensorNoteView {
	return &sensorNoteView{Text: n.Text, By: n.ByName, At: n.UpdatedAt}
}

// sensorNoteKey is the key a note is filed under: the sensor's item id, or for a problem with no
// sensor (an unreachable interface), the problem's id - the same id its census row carries.
func sensorNoteKey(itemID, eventID string) string {
	if itemID != "" {
		return itemID
	}
	return eventID
}

// userLabel is how a note names its author: their name, or their email without one.
func userLabel(u *store.User) string {
	if u == nil {
		return ""
	}
	if n := strings.TrimSpace(u.Name + " " + u.Surname); n != "" {
		return n
	}
	return u.Email
}

// censusRowByKey finds a sensor's census row by its key.
func (s *Server) censusRowByKey(ctx context.Context, key string) (sensorRow, bool, error) {
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		return sensorRow{}, false, err
	}
	for _, r := range rows {
		if r.ItemID == key {
			return r, true, nil
		}
	}
	return sensorRow{}, false, nil
}

// PUT /api/sensors/{key}/note {"text": "..."} (admin, helpdesk) - write the note on a sensor with an
// open problem; an empty text takes it off.
func (s *Server) handleSetSensorNote(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sensorNoteKeyShape.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a sensor"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		s.handleClearSensorNote(w, r)
		return
	}
	if utf8.RuneCountInString(text) > sensorNoteMax {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a note is at most 500 characters"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), censusBuildTime)
	defer cancel()
	row, ok, err := s.censusRowByKey(ctx, key)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if !ok || !s.hostInScope(ctx, scopeFrom(r), row.HostID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	if len(row.EventIDs) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this sensor has no open problem: a note stays only until the sensor is OK, so it goes on a sensor in trouble"})
		return
	}
	caller, _ := auth.UserFrom(r.Context())
	var by int64
	if caller != nil {
		by = caller.ID
	}
	n, err := s.st.SetSensorNote(ctx, key, row.HostID, text, by, userLabel(caller), time.Now().Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, noteViewOf(n))
}

// DELETE /api/sensors/{key}/note (admin, helpdesk) - take a sensor's note off (kept for the history).
func (s *Server) handleClearSensorNote(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sensorNoteKeyShape.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a sensor"})
		return
	}
	notes, err := s.st.LiveSensorNotes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	n, ok := notes[key]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}) // nothing to take off
		return
	}
	ctx, cancel := scopeLookupCtx(r)
	inScope := s.hostInScope(ctx, scopeFrom(r), n.HostID)
	cancel()
	if !inScope {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	if err := s.st.ClearSensorNote(r.Context(), key, time.Now().Unix()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// openSensorKeys is every sensor with an open problem (warning or worse), by note key, and whether the
// answer is complete: a problem whose trigger couldn't be read leaves it unknown, and then no note
// may be cleared.
func openSensorKeys(problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) (map[string]bool, bool) {
	open := map[string]bool{}
	for _, p := range problems {
		if atoi(p.Severity) < 2 {
			continue
		}
		t, ok := targets[p.ObjectID]
		if !ok {
			return nil, false
		}
		if len(t.Items) == 0 {
			open[sensorNoteKey("", p.EventID)] = true
			continue
		}
		for _, it := range t.Items {
			open[sensorNoteKey(it.ItemID, "")] = true
		}
	}
	return open, true
}

// noteSweep decides what happens to each shown note this notifier tick: a sensor seen OK starts its
// countdown (okSince), one in trouble again stops it (reopen), and one OK for the grace period is
// cleared as of when it went OK. The grace (the alert delay, at least the hold grace) keeps a note
// through a blip.
type noteSweep struct {
	okSince []int64 // note ids first seen OK now
	reopen  []int64 // note ids whose sensor has a problem again
	clear   map[string]int64
}

func planNoteSweep(notes map[string]store.SensorNote, open map[string]bool, grace, now int64) noteSweep {
	out := noteSweep{clear: map[string]int64{}}
	for key, n := range notes {
		switch {
		case open[key]:
			if n.OKSince != 0 {
				out.reopen = append(out.reopen, n.ID)
			}
		case n.OKSince == 0:
			out.okSince = append(out.okSince, n.ID)
		case now-n.OKSince >= grace:
			out.clear[key] = n.OKSince
		}
	}
	return out
}

// sweepSensorNotes applies a tick's plan to the store.
func sweepSensorNotes(ctx context.Context, st *store.Store, notes map[string]store.SensorNote, problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget, grace, now int64) {
	if len(notes) == 0 {
		return
	}
	open, complete := openSensorKeys(problems, targets)
	if !complete {
		return
	}
	plan := planNoteSweep(notes, open, grace, now)
	for _, id := range plan.okSince {
		_ = st.SetSensorNoteOKSince(ctx, id, now)
	}
	for _, id := range plan.reopen {
		_ = st.SetSensorNoteOKSince(ctx, id, 0)
	}
	for key, at := range plan.clear {
		_ = st.ClearSensorNote(ctx, key, at)
	}
}

// noteFor is a sensor's shown note text and author, for an alert.
func noteFor(notes map[string]store.SensorNote, itemID, eventID string) (string, string) {
	n, ok := notes[sensorNoteKey(itemID, eventID)]
	if !ok {
		return "", ""
	}
	return n.Text, n.ByName
}
