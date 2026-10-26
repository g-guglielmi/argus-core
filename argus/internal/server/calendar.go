// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

// The maintenance calendar (DESIGN section 9): the Maintenance page's month view reads each window's
// runs from /api/maintenance/occurrences, and each user has a calendar feed, a secret link to an .ics
// of the windows they see, for Outlook or Google Calendar. Each run is its own event with UTC times,
// so a calendar never has to agree with Argus about daylight saving.

const (
	calPast   = 30 * 24 * time.Hour  // runs a feed keeps from the past
	calAhead  = 180 * 24 * time.Hour // and lists ahead
	calMaxRng = 62 * 24 * time.Hour  // what the month view asks for at most
)

// calOccurrence is one run of a window.
type calOccurrence struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// windowRuns lists the runs of the enabled windows sc sees in [from, to), by start.
func (s *Server) windowRuns(ctx context.Context, sc siteScope, from, to time.Time) ([]calOccurrence, []store.MaintenanceWindow, error) {
	windows, err := s.st.MaintenanceWindows(ctx)
	if err != nil {
		return nil, nil, err
	}
	loc := s.location()
	out := []calOccurrence{}
	var seen []store.MaintenanceWindow
	for _, w := range windows {
		if !w.Enabled {
			continue
		}
		if see, _ := s.windowInScope(ctx, sc, w); !see {
			continue
		}
		seen = append(seen, w)
		for _, sp := range occurrencesBetween(w, from, to, loc) {
			out = append(out, calOccurrence{ID: w.ID, Name: w.Name, Start: sp.From.Unix(), End: sp.To.Unix()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, seen, nil
}

// GET /api/maintenance/occurrences?from=&to= (unix s): the runs of the windows the user sees.
func (s *Server) handleMaintOccurrences(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if from <= 0 || to <= from || time.Duration(to-from)*time.Second > calMaxRng {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from and to must be a span of up to 62 days"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runs, _, err := s.windowRuns(ctx, scopeFrom(r), time.Unix(from, 0), time.Unix(to, 0))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the maintenance windows"})
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) calendarLink(token string) string {
	return strings.TrimRight(s.mgr.PublicURL(), "/") + "/cal/m/" + token + ".ics"
}

// GET /api/me/calendar-feed: the user's feed link, made on first ask.
func (s *Server) handleMyCalendarFeed(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	tok, err := s.st.UserCalendarToken(r.Context(), u.ID)
	if errors.Is(err, store.ErrNotFound) {
		tok = newStatusToken()
		err = s.st.SetUserCalendarToken(r.Context(), u.ID, tok)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": s.calendarLink(tok), "public_url": s.mgr.PublicURL() != ""})
}

// POST /api/me/calendar-feed/rotate: a new link; the old one stops working at once.
func (s *Server) handleRotateCalendarFeed(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	tok := newStatusToken()
	if err := s.st.SetUserCalendarToken(r.Context(), u.ID, tok); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": s.calendarLink(tok), "public_url": s.mgr.PublicURL() != ""})
}

// GET /cal/m/{file} (public: the token is the credential): the .ics of the windows the link's user
// sees. An unknown token and a disabled user both answer 404.
func (s *Server) handleCalendarICS(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutSuffix(r.PathValue("file"), ".ics")
	if !ok || tok == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	u, err := s.st.UserByCalendarToken(ctx, tok)
	if err != nil || u.Disabled {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	runs, windows, err := s.windowRuns(ctx, scopeOf(u), now.Add(-calPast), now.Add(calAhead))
	if err != nil {
		http.Error(w, "could not read the maintenance windows", http.StatusInternalServerError)
		return
	}
	idx, _ := s.hostIndex(ctx)
	covers := map[int64]string{}
	for _, mw := range windows {
		parts := append([]string{}, mw.Sites...)
		for _, h := range mw.HostIDs {
			if hi, ok := idx[h]; ok {
				parts = append(parts, hi.Name)
			}
		}
		covers[mw.ID] = strings.Join(parts, ", ")
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(buildICS(runs, covers, now)))
}

// buildICS writes the runs as an iCalendar (RFC 5545): one event per run, UTC times, a stable UID per
// run so an edit replaces it.
func buildICS(runs []calOccurrence, covers map[int64]string, now time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(foldICS(s)); b.WriteString("\r\n") }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Argus//Maintenance windows//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:Argus maintenance")
	line("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	line("X-PUBLISHED-TTL:PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, o := range runs {
		line("BEGIN:VEVENT")
		line(fmt.Sprintf("UID:argus-maintenance-%d-%d@argus", o.ID, o.Start))
		line("DTSTAMP:" + stamp)
		line("DTSTART:" + time.Unix(o.Start, 0).UTC().Format("20060102T150405Z"))
		line("DTEND:" + time.Unix(o.End, 0).UTC().Format("20060102T150405Z"))
		line("SUMMARY:" + escICS("Maintenance: "+o.Name))
		desc := "Alerts for these hosts wait during the window; whatever is still wrong when it ends is alerted."
		if c := covers[o.ID]; c != "" {
			desc = "Covers " + c + ". " + desc
		}
		line("DESCRIPTION:" + escICS(desc))
		line("TRANSP:TRANSPARENT")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

// escICS escapes a TEXT value.
func escICS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)
	return r.Replace(s)
}

// foldICS folds a content line at 75 octets, never inside a UTF-8 character.
func foldICS(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = 74 // the leading space counts
	}
	b.WriteString(s)
	return b.String()
}
