// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"argus/internal/store"
)

// One timeline per incident (DESIGN section 7b): what happened to it in order, so "did anyone get
// paged?" has an answer. The notifier records when it opened, each channel it alerted (or couldn't:
// the channel's error), escalated to or reminded, a hold behind a down master, a wait for a
// maintenance window or for a problem heard of late, the channels told of an acknowledgement, and the
// end; the handlers record the acknowledgements and the notes. Kept a year (incident_log).

const incidentLogKeep = 365 * 24 * time.Hour

// tlog records a timeline entry; a failure only costs the entry.
func tlog(ctx context.Context, st *store.Store, eventID, kind, who, channel, detail string, at int64) {
	if st == nil || eventID == "" {
		return
	}
	if at == 0 {
		at = time.Now().Unix()
	}
	_ = st.AddIncidentLog(ctx, store.IncidentLogEntry{EventID: eventID, At: at, Kind: kind, Who: who, Channel: channel, Detail: detail})
}

// timelineMaint remembers the incidents already logged as waiting for a maintenance window, so each
// wait is one entry (a restart may log it once more).
var timelineMaint = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

func tlogMaintenance(ctx context.Context, st *store.Store, eventID, window string) {
	timelineMaint.Lock()
	seen := timelineMaint.m[eventID]
	timelineMaint.m[eventID] = true
	timelineMaint.Unlock()
	if !seen {
		tlog(ctx, st, eventID, "maintenance", "", "", window, 0)
	}
}

func forgetTimelineState(eventID string) {
	timelineMaint.Lock()
	delete(timelineMaint.m, eventID)
	timelineMaint.Unlock()
}

var timelinePrune struct {
	sync.Mutex
	last time.Time
}

// pruneIncidentLogDaily drops the timeline entries past a year, at most once a day.
func pruneIncidentLogDaily(ctx context.Context, st *store.Store) {
	timelinePrune.Lock()
	due := time.Since(timelinePrune.last) > 24*time.Hour
	if due {
		timelinePrune.last = time.Now()
	}
	timelinePrune.Unlock()
	if due {
		_ = st.PruneIncidentLog(ctx, time.Now().Add(-incidentLogKeep).Unix())
	}
}

// GET /api/incidents/{id}/timeline: an incident's timeline (empty for one from before Argus kept them).
func (s *Server) handleIncidentTimeline(w http.ResponseWriter, r *http.Request) {
	entries, err := s.st.IncidentLog(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// severityName is a Zabbix severity as the timeline reads it.
func severityName(sev int) string {
	switch sev {
	case 5:
		return "Disaster"
	case 4:
		return "High"
	case 3:
		return "Average"
	case 2:
		return "Warning"
	}
	return "Information"
}

// channelTypeName is a channel type as people say it.
func channelTypeName(t string) string {
	switch t {
	case "email":
		return "Email"
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "teams":
		return "Teams"
	case "slack":
		return "Slack"
	case "ntfy":
		return "ntfy"
	case "gotify":
		return "Gotify"
	case "pushover":
		return "Pushover"
	case "webhook":
		return "webhook"
	}
	return t
}

// fmtMinutesGo is a channel's delay in minutes as the timeline reads it: 30 min, 2 h, 1 h 30 min.
func fmtMinutesGo(m int64) string { return humanDur(m * 60) }

// ackWho names who acknowledged: the user, or the alert's link.
func (s *Server) ackWho(ctx context.Context, by int64) string {
	if by == 0 {
		return "an alert's acknowledge link"
	}
	if u, err := s.st.UserByID(ctx, by); err == nil {
		return userLabel(u)
	}
	return "a user"
}
