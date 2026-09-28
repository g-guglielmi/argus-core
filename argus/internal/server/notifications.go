// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"argus/internal/notify"
	"argus/internal/store"
)

type channelView struct {
	ID          int64             `json:"id"`
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Enabled     bool              `json:"enabled"`
	Sites       []string          `json:"sites"`
	MinSeverity int               `json:"min_severity"`
	DelayMin    int               `json:"delay_min"`
	RepeatMin   int               `json:"repeat_min"`
	RepeatSev   int               `json:"repeat_min_severity"`
	Alerts      bool              `json:"alerts"`
	Notices     bool              `json:"system_notices"`
	Config      map[string]string `json:"config"`
	// Delivery health for the channel card: last successful send, last failure (+ reason), sent count.
	LastSentAt  int64  `json:"last_sent_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt int64  `json:"last_error_at,omitempty"`
	SentCount   int64  `json:"sent_count,omitempty"`
}

func toChannelView(c store.NotifyChannel) channelView {
	cfg := c.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	return channelView{
		ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled, Sites: c.Sites, MinSeverity: c.MinSeverity,
		DelayMin: c.DelayMin, RepeatMin: c.RepeatMin, RepeatSev: c.RepeatSev, Alerts: c.Alerts, Notices: c.Notices, Config: cfg,
		LastSentAt: c.LastSentAt, LastError: c.LastError, LastErrorAt: c.LastErrorAt, SentCount: c.SentCount,
	}
}

var validChannelTypes = map[string]bool{"discord": true, "telegram": true, "email": true}

// cleanSites trims and drops empty entries from a submitted site list. An empty result means the
// channel serves all sites. Shared by the admin and personal channel editors.
func cleanSites(sites []string) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.st.ListNotifyChannels(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	out := make([]channelView, 0, len(chans))
	for _, c := range chans {
		out = append(out, toChannelView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// channelRequest is the create/update body.
type channelRequest struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Enabled     bool              `json:"enabled"`
	Sites       []string          `json:"sites"`
	MinSeverity int               `json:"min_severity"`
	DelayMin    int               `json:"delay_min"`
	RepeatMin   int               `json:"repeat_min"`
	RepeatSev   int               `json:"repeat_min_severity"`
	Alerts      *bool             `json:"alerts"`         // nil = true (a client from before system notices)
	Notices     bool              `json:"system_notices"`
	Config      map[string]string `json:"config"`
}

// channelCarries resolves what a channel carries and rejects one that would carry nothing.
func channelCarries(alerts *bool, notices bool) (bool, bool, string) {
	a := alerts == nil || *alerts
	if !a && !notices {
		return false, false, "a channel needs alerts, system notices, or both"
	}
	return a, notices, ""
}

// Escalation bounds: a channel can wait up to a day before it's told, and reminds at most every 5
// minutes (a tighter loop is noise, since the notifier polls every 30 s anyway) and at least daily.
const (
	maxChannelDelayMin  = 1440
	minChannelRepeatMin = 5
	maxChannelRepeatMin = 1440
)

// clampEscalation bounds a channel's "notify after" and "remind every" minutes (0 = off for both).
func clampEscalation(delay, repeat int) (int, int) {
	if delay < 0 {
		delay = 0
	} else if delay > maxChannelDelayMin {
		delay = maxChannelDelayMin
	}
	switch {
	case repeat <= 0:
		repeat = 0
	case repeat < minChannelRepeatMin:
		repeat = minChannelRepeatMin
	case repeat > maxChannelRepeatMin:
		repeat = maxChannelRepeatMin
	}
	return delay, repeat
}

// Alert levels a channel can choose, as Zabbix severity floors. The app shows problems as warnings
// (Zabbix Warning) or errors (Average, High, Disaster), so a channel picks one of those two:
// "warnings and errors" or "errors only". Stricter floors (High, Disaster) would silently skip some
// errors, like an Average "endpoint down", so they're folded into "errors only".
const (
	levelWarnings = 2 // Warning and up
	levelErrors   = 3 // Average and up: everything the app shows as an error
)

// alertLevel maps a requested severity floor onto the two levels (0 = unset = warnings and errors).
func alertLevel(sev int) int {
	if sev >= levelErrors {
		return levelErrors
	}
	return levelWarnings
}

func (req channelRequest) validate() (store.NotifyChannel, string) {
	t := strings.TrimSpace(req.Type)
	if !validChannelTypes[t] {
		return store.NotifyChannel{}, "type must be discord, telegram or email"
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return store.NotifyChannel{}, "name is required"
	}
	cfg := req.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	// Email channels pick a recipients mode: "fixed" (the set `to` address, default) or "users" (fan
	// out to every active user's registered email). Other types ignore the key.
	if t == "email" {
		switch cfg["recipients"] {
		case "", "fixed", "users":
		default:
			return store.NotifyChannel{}, "recipients must be 'fixed' or 'users'"
		}
	}
	// The notifier never alerts below Warning, so clamp the floor to 2..5 (Warning..Disaster).
	sev := alertLevel(req.MinSeverity)
	delay, repeat := clampEscalation(req.DelayMin, req.RepeatMin)
	alerts, notices, msg := channelCarries(req.Alerts, req.Notices)
	if msg != "" {
		return store.NotifyChannel{}, msg
	}
	return store.NotifyChannel{
		Type: t, Name: name, Enabled: req.Enabled, Sites: cleanSites(req.Sites), MinSeverity: sev,
		DelayMin: delay, RepeatMin: repeat, RepeatSev: alertLevel(req.RepeatSev), Alerts: alerts, Notices: notices, Config: cfg,
	}, ""
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ch, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	id, err := s.st.CreateNotifyChannel(r.Context(), ch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	ch.ID = id
	writeJSON(w, http.StatusOK, toChannelView(ch))
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if _, err := s.st.GetNotifyChannel(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return
	}
	var req channelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	ch, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ch.ID = id
	if err := s.st.UpdateNotifyChannel(r.Context(), ch); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, toChannelView(ch))
}

func (s *Server) handleSetChannelEnabled(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.st.SetNotifyChannelEnabled(r.Context(), id, req.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if err := s.st.DeleteNotifyChannel(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestChannel sends a sample notification through a saved channel so the admin can
// confirm the credentials work end-to-end.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	ch, err := s.st.GetNotifyChannel(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ev := notify.SampleEvent(time.Now().In(s.mgr.Location()), s.mgr.PublicURL())
	dr, dg, db := statusRGB(ev.State)
	ev.ChartPNG = renderChart(demoSeries(), dr, dg, db, "", demoThresholds()) // preview the graph too
	err = notify.Send(ctx, toNotifyChannel(*ch), ev)
	// A test counts as a delivery attempt too, so the card's health line reflects it either way.
	_ = s.st.RecordNotifyDelivery(ctx, id, err)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// groupAncestors returns the ancestor paths of a '/'-hierarchical Zabbix host-group name: "a/b/c"
// yields ["a", "a/b"]. A top-level name (no "/") has none.
func groupAncestors(name string) []string {
	var out []string
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			out = append(out, name[:i])
		}
	}
	return out
}

// handleNotifySites returns the Zabbix host-group names for the channel "site" picker: every group
// that has hosts, plus each group's ancestor paths - so a probe's root group (e.g. "site1") is
// selectable even when only its subgroups hold hosts, and selecting it covers them (see siteCovers).
func (s *Server) handleNotifySites(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		for _, g := range h.Groups {
			seen[g.Name] = true
			for _, anc := range groupAncestors(g.Name) {
				seen[anc] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, out)
}
