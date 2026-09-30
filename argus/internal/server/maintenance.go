// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// Maintenance windows: planned stretches of time (a nightly backup, a monthly parity check, a patch
// night) when some hosts' alerts are held. Zabbix keeps collecting and the problems stay on screen,
// tagged with the window; the notifier sends nothing for them until the window ends, then alerts what
// is still open, as after a pause. Schedules run in the Argus timezone. DESIGN section 9.

const (
	maintMaxDuration = 7 * 24 * 60 // minutes
	maintCacheTTL    = 30 * time.Second
)

// maintHit is the window a host is in right now.
type maintHit struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Until int64  `json:"until"` // unix s
}

// occurrenceAt returns the start of the window's occurrence that covers t, if one does.
func occurrenceAt(w store.MaintenanceWindow, t time.Time, loc *time.Location) (time.Time, bool) {
	dur := time.Duration(w.DurationMin) * time.Minute
	if w.Kind == "once" {
		start := time.Unix(w.StartAt, 0)
		return start, w.StartAt > 0 && !t.Before(start) && t.Before(start.Add(dur))
	}
	// An occurrence covering t started at most dur before it: look at the days from then to t.
	lt := t.In(loc)
	first := lt.Add(-dur)
	day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc)
	for ; !day.After(lt); day = day.AddDate(0, 0, 1) {
		if !runsOn(w, day) {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), w.Minute/60, w.Minute%60, 0, 0, loc)
		if !t.Before(start) && t.Before(start.Add(dur)) {
			return start, true
		}
	}
	return time.Time{}, false
}

// runsOn reports whether a recurring window starts on this (local) day.
func runsOn(w store.MaintenanceWindow, day time.Time) bool {
	switch w.Kind {
	case "daily":
		return true
	case "weekly":
		return w.Weekdays&(1<<uint(day.Weekday())) != 0
	case "monthly":
		last := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, day.Location()).Day()
		want := w.MonthDay
		if want == -1 || want > last {
			want = last
		}
		return day.Day() == want
	}
	return false
}

// nextStart is when the window next begins after t (zero when it never will: a past one-off).
func nextStart(w store.MaintenanceWindow, t time.Time, loc *time.Location) time.Time {
	if w.Kind == "once" {
		if s := time.Unix(w.StartAt, 0); s.After(t) {
			return s
		}
		return time.Time{}
	}
	lt := t.In(loc)
	day := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 400; i++ {
		if runsOn(w, day) {
			if s := time.Date(day.Year(), day.Month(), day.Day(), w.Minute/60, w.Minute%60, 0, 0, loc); s.After(t) {
				return s
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}
}

// windowCovers reports whether a window applies to a host (by id, or by one of its groups).
func windowCovers(w store.MaintenanceWindow, hostID string, groups []string) bool {
	for _, id := range w.HostIDs {
		if id == hostID {
			return true
		}
	}
	for _, s := range w.Sites {
		for _, g := range groups {
			if siteCovers(s, g) {
				return true
			}
		}
	}
	return false
}

// maintenanceHits is every host in an active window right now, with the window (the one ending last
// when several overlap).
func maintenanceHits(windows []store.MaintenanceWindow, hostGroups map[string][]string, loc *time.Location, now time.Time) map[string]maintHit {
	out := map[string]maintHit{}
	for _, w := range windows {
		if !w.Enabled {
			continue
		}
		start, on := occurrenceAt(w, now, loc)
		if !on {
			continue
		}
		until := start.Add(time.Duration(w.DurationMin) * time.Minute).Unix()
		hit := maintHit{ID: w.ID, Name: w.Name, Until: until}
		for hostID, groups := range hostGroups {
			if windowCovers(w, hostID, groups) {
				if cur, has := out[hostID]; !has || until > cur.Until {
					out[hostID] = hit
				}
			}
		}
		// A host listed by id that the group map doesn't know yet still counts.
		for _, id := range w.HostIDs {
			if _, has := out[id]; !has {
				out[id] = hit
			}
		}
	}
	return out
}

// maintCache keeps the current hits for the views (the notifier computes its own each tick).
type maintCache struct {
	mu   sync.Mutex
	at   time.Time
	hits map[string]maintHit
}

// maintenanceNow returns the hosts in maintenance right now (best effort: an error means none).
func (s *Server) maintenanceNow(ctx context.Context) map[string]maintHit {
	s.maint.mu.Lock()
	defer s.maint.mu.Unlock()
	if s.maint.hits != nil && time.Since(s.maint.at) < maintCacheTTL {
		return s.maint.hits
	}
	windows, err := s.st.MaintenanceWindows(ctx)
	if err != nil {
		return map[string]maintHit{}
	}
	if len(windows) == 0 {
		s.maint.hits, s.maint.at = map[string]maintHit{}, time.Now()
		return s.maint.hits
	}
	groups := map[string][]string{}
	if g, err := s.hostGroupMap(ctx); err == nil {
		groups = g
	}
	s.maint.hits, s.maint.at = maintenanceHits(windows, groups, s.location(), time.Now()), time.Now()
	return s.maint.hits
}

// location is the Argus timezone (UTC for a Server assembled by hand, in tests).
func (s *Server) location() *time.Location {
	if s.mgr == nil {
		return time.UTC
	}
	return s.mgr.Location()
}

func (s *Server) invalidateMaintenance() {
	s.maint.mu.Lock()
	s.maint.hits = nil
	s.maint.mu.Unlock()
}

// --- API -----------------------------------------------------------------------------------------

type maintView struct {
	store.MaintenanceWindow
	Active    bool  `json:"active"`
	Until     int64 `json:"until,omitempty"`      // the current occurrence's end, while active
	NextStart int64 `json:"next_start,omitempty"` // the next occurrence's start
}

// validateWindow checks a window's settings and returns what is wrong ("" when fine).
func validateWindow(w *store.MaintenanceWindow) string {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" || len([]rune(w.Name)) > 80 {
		return "a name (up to 80 characters) is required"
	}
	w.Sites = cleanSites(w.Sites)
	var ids []string
	for _, id := range w.HostIDs {
		if id = strings.TrimSpace(id); id != "" {
			if _, err := strconv.ParseUint(id, 10, 64); err != nil {
				return "invalid host id"
			}
			ids = append(ids, id)
		}
	}
	w.HostIDs = ids
	if len(w.Sites) == 0 && len(w.HostIDs) == 0 {
		return "pick at least one site or host"
	}
	if w.DurationMin < 5 || w.DurationMin > maintMaxDuration {
		return "the duration must be between 5 minutes and 7 days"
	}
	if w.Kind != "once" && (w.Minute < 0 || w.Minute > 24*60-1) {
		return "the start time must be a time of day"
	}
	switch w.Kind {
	case "once":
		if w.StartAt <= 0 {
			return "pick when it starts"
		}
		w.Minute, w.Weekdays, w.MonthDay = 0, 0, 0
	case "daily":
		w.Weekdays, w.MonthDay, w.StartAt = 0, 0, 0
	case "weekly":
		if w.Weekdays <= 0 || w.Weekdays > 127 {
			return "pick at least one weekday"
		}
		w.MonthDay, w.StartAt = 0, 0
	case "monthly":
		if w.MonthDay != -1 && (w.MonthDay < 1 || w.MonthDay > 31) {
			return "the day of the month must be 1-31, or the last day"
		}
		w.Weekdays, w.StartAt = 0, 0
	default:
		return `kind must be "once", "daily", "weekly" or "monthly"`
	}
	return ""
}

// windowInScope reports whether a scoped user may see a window (it touches one of their sites or
// hosts) and change it (everything it covers is theirs).
func (s *Server) windowInScope(ctx context.Context, sc siteScope, w store.MaintenanceWindow) (see, edit bool) {
	if sc.all {
		return true, true
	}
	see, edit = false, true
	for _, site := range w.Sites {
		if sc.coversGroup(site) {
			see = true
		} else {
			edit = false
			if sc.narrowsTo(site) { // a window on "site1" reaches a user limited to "site1/Network"
				see = true
			}
		}
	}
	for _, id := range w.HostIDs {
		if s.hostInScope(ctx, sc, id) {
			see = true
		} else {
			edit = false
		}
	}
	return see, edit && see
}

// narrowsTo reports whether a site contains one of the scope's sites (a window on "site1" reaches a
// user limited to "site1/Network").
func (sc siteScope) narrowsTo(site string) bool {
	for _, s := range sc.sites {
		if siteCovers(site, s) {
			return true
		}
	}
	return false
}

func (s *Server) maintViewOf(w store.MaintenanceWindow, now time.Time) maintView {
	v := maintView{MaintenanceWindow: w}
	if v.Sites == nil {
		v.Sites = []string{}
	}
	if v.HostIDs == nil {
		v.HostIDs = []string{}
	}
	loc := s.location()
	if start, on := occurrenceAt(w, now, loc); on && w.Enabled {
		v.Active = true
		v.Until = start.Add(time.Duration(w.DurationMin) * time.Minute).Unix()
	}
	if n := nextStart(w, now, loc); !n.IsZero() {
		v.NextStart = n.Unix()
	}
	return v
}

// handleListMaintenance serves GET /api/maintenance: the windows the user may see.
func (s *Server) handleListMaintenance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	windows, err := s.st.MaintenanceWindows(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the maintenance windows"})
		return
	}
	sc := scopeFrom(r)
	now := time.Now()
	out := []maintView{}
	for _, mw := range windows {
		if see, _ := s.windowInScope(ctx, sc, mw); see {
			out = append(out, s.maintViewOf(mw, now))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Active && !out[j].Active })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decodeWindow(w http.ResponseWriter, r *http.Request) (store.MaintenanceWindow, bool) {
	var req store.MaintenanceWindow
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return req, false
	}
	if msg := validateWindow(&req); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return req, false
	}
	return req, true
}

// handleCreateMaintenance serves POST /api/maintenance (admin, helpdesk).
func (s *Server) handleCreateMaintenance(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeWindow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, edit := s.windowInScope(ctx, scopeFrom(r), req); !edit {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a window can only cover your own sites and hosts"})
		return
	}
	if u, _ := auth.UserFrom(r.Context()); u != nil {
		req.CreatedBy = u.Email
	}
	id, err := s.st.CreateMaintenanceWindow(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the window"})
		return
	}
	s.invalidateMaintenance()
	s.logger.Info("maintenance window created", "id", id, "name", req.Name, "schedule", describeWindow(req))
	req.ID = id
	writeJSON(w, http.StatusOK, s.maintViewOf(req, time.Now()))
}

// handleUpdateMaintenance serves PATCH /api/maintenance/{id} (admin, helpdesk).
func (s *Server) handleUpdateMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	req, ok := s.decodeWindow(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cur, err := s.st.MaintenanceWindow(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "window not found"})
		return
	}
	sc := scopeFrom(r)
	if see, edit := s.windowInScope(ctx, sc, cur); !see {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "window not found"})
		return
	} else if !edit {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this window also covers sites or hosts outside yours"})
		return
	}
	if _, edit := s.windowInScope(ctx, sc, req); !edit {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a window can only cover your own sites and hosts"})
		return
	}
	req.ID, req.CreatedBy, req.CreatedAt = id, cur.CreatedBy, cur.CreatedAt
	if err := s.st.UpdateMaintenanceWindow(ctx, req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the window"})
		return
	}
	s.invalidateMaintenance()
	writeJSON(w, http.StatusOK, s.maintViewOf(req, time.Now()))
}

// handleDeleteMaintenance serves DELETE /api/maintenance/{id} (admin, helpdesk).
func (s *Server) handleDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cur, err := s.st.MaintenanceWindow(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "window not found"})
		return
	}
	if see, edit := s.windowInScope(ctx, scopeFrom(r), cur); !see {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "window not found"})
		return
	} else if !edit {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this window also covers sites or hosts outside yours"})
		return
	}
	if err := s.st.DeleteMaintenanceWindow(ctx, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the window"})
		return
	}
	s.invalidateMaintenance()
	s.logger.Info("maintenance window deleted", "id", id, "name", cur.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// describeWindow words a window's schedule ("Sundays 02:00 for 2 h"), for the log and tests.
func describeWindow(w store.MaintenanceWindow) string {
	hm := fmt.Sprintf("%02d:%02d", w.Minute/60, w.Minute%60)
	dur := fmt.Sprintf("%d min", w.DurationMin)
	if w.DurationMin%60 == 0 {
		dur = fmt.Sprintf("%d h", w.DurationMin/60)
	}
	switch w.Kind {
	case "daily":
		return "every day " + hm + " for " + dur
	case "weekly":
		var days []string
		for i, d := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
			if w.Weekdays&(1<<uint(i)) != 0 {
				days = append(days, d)
			}
		}
		return strings.Join(days, ", ") + " " + hm + " for " + dur
	case "monthly":
		if w.MonthDay == -1 {
			return "the last day of the month " + hm + " for " + dur
		}
		return fmt.Sprintf("day %d of the month %s for %s", w.MonthDay, hm, dur)
	}
	return "once, for " + dur
}
