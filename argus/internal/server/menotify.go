// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"argus/internal/auth"
	"argus/internal/notify"
	"argus/internal/store"
)

// Personal (per-user) notification channels. Every signed-in user (any role) manages their own
// destinations here (the public services in notify.PersonalTypes, sent to public addresses only), under /api/me/notify/*; they never see or touch another user's
// channels or the admin/global channels. A personal channel is the same editable config a global
// channel uses (reusing the leaf notify.Send), scoped to the caller.

type userChannelView struct {
	ID          int64             `json:"id"`
	Type        string            `json:"type"`
	Enabled     bool              `json:"enabled"`
	Sites       []string          `json:"sites"`
	MinSeverity int               `json:"min_severity"`
	DelayMin    int               `json:"delay_min"`
	RepeatMin   int               `json:"repeat_min"`
	RepeatSev   int               `json:"repeat_min_severity"`
	Alerts      bool              `json:"alerts"`
	Notices     bool              `json:"system_notices"`
	Config      map[string]string `json:"config"`
	// Delivery health for the card: last successful send, last failure (+ reason), sent count.
	LastSentAt  int64  `json:"last_sent_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt int64  `json:"last_error_at,omitempty"`
	SentCount   int64  `json:"sent_count,omitempty"`
}

func toUserChannelView(c store.UserNotifyChannel) userChannelView {
	cfg := maskChannelConfig(c.Config)
	return userChannelView{
		ID: c.ID, Type: c.Type, Enabled: c.Enabled, Sites: c.Sites, MinSeverity: c.MinSeverity,
		DelayMin: c.DelayMin, RepeatMin: c.RepeatMin, RepeatSev: c.RepeatSev, Alerts: c.Alerts, Notices: c.Notices, Config: cfg,
		LastSentAt: c.LastSentAt, LastError: c.LastError, LastErrorAt: c.LastErrorAt, SentCount: c.SentCount,
	}
}

type userChannelRequest struct {
	Type        string            `json:"type"`
	Enabled     bool              `json:"enabled"`
	Sites       []string          `json:"sites"`
	MinSeverity int               `json:"min_severity"`
	DelayMin    int               `json:"delay_min"`
	RepeatMin   int               `json:"repeat_min"`
	RepeatSev   int               `json:"repeat_min_severity"`
	Alerts      *bool             `json:"alerts"` // nil = true
	Notices     bool              `json:"system_notices"`
	Config      map[string]string `json:"config"`
}

func (req userChannelRequest) validate() (store.UserNotifyChannel, string) {
	t := strings.TrimSpace(req.Type)
	if !notify.PersonalTypes[t] {
		return store.UserNotifyChannel{}, "a personal channel can't be of this type"
	}
	cfg := req.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	// Require the destination keys up front so a user gets a clear message rather than a silent no-send.
	if msg := notify.CheckConfig(t, cfg, true); msg != "" {
		return store.UserNotifyChannel{}, msg
	}
	// The notifier never alerts below Warning, so clamp the floor to 2..5 (Warning..Disaster).
	sev := alertLevel(req.MinSeverity)
	delay, repeat := clampEscalation(req.DelayMin, req.RepeatMin)
	alerts, notices, msg := channelCarries(req.Alerts, req.Notices)
	if msg != "" {
		return store.UserNotifyChannel{}, msg
	}
	return store.UserNotifyChannel{
		Type: t, Enabled: req.Enabled, Sites: cleanSites(req.Sites), MinSeverity: sev,
		DelayMin: delay, RepeatMin: repeat, RepeatSev: alertLevel(req.RepeatSev), Alerts: alerts, Notices: notices, Config: cfg,
	}, ""
}

// myChannel loads the path-id channel and confirms it belongs to the caller; on any mismatch it writes
// 404 (not 403) so a user can't probe which channel ids exist for other users.
func (s *Server) myChannel(w http.ResponseWriter, r *http.Request) (*store.UserNotifyChannel, bool) {
	caller, _ := auth.UserFrom(r.Context())
	ch, err := s.st.GetUserNotifyChannel(r.Context(), atoi64(r.PathValue("id")))
	if err != nil || caller == nil || ch.UserID != caller.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
		return nil, false
	}
	return ch, true
}

func (s *Server) handleListMyChannels(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	chans, err := s.st.ListUserNotifyChannels(r.Context(), caller.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	out := make([]userChannelView, 0, len(chans))
	for _, c := range chans {
		out = append(out, toUserChannelView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateMyChannel(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.UserFrom(r.Context())
	var req userChannelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Config = keepChannelSecrets(req.Config, nil)
	ch, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := outsideSites(scopeFrom(r), ch.Sites); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ch.UserID = caller.ID
	id, err := s.st.CreateUserNotifyChannel(r.Context(), ch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	ch.ID = id
	writeJSON(w, http.StatusOK, toUserChannelView(ch))
}

func (s *Server) handleUpdateMyChannel(w http.ResponseWriter, r *http.Request) {
	existing, ok := s.myChannel(w, r)
	if !ok {
		return
	}
	var req userChannelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Config = keepChannelSecrets(req.Config, existing.Config)
	ch, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if msg := outsideSites(scopeFrom(r), ch.Sites); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ch.ID = existing.ID
	ch.UserID = existing.UserID
	if err := s.st.UpdateUserNotifyChannel(r.Context(), ch); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, toUserChannelView(ch))
}

func (s *Server) handleSetMyChannelEnabled(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.myChannel(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.st.SetUserNotifyChannelEnabled(r.Context(), ch.ID, req.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

func (s *Server) handleDeleteMyChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.myChannel(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteUserNotifyChannel(r.Context(), ch.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestMyChannel sends a sample notification through the caller's channel so they can confirm
// their own bot/webhook works end-to-end, recording the outcome on the channel's health line.
func (s *Server) handleTestMyChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.myChannel(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ev := notify.SampleEvent(time.Now().In(s.mgr.Location()), s.mgr.PublicURL())
	dr, dg, db := statusRGB(ev.State)
	ev.ChartPNG = renderChart(demoSeries(), dr, dg, db, "", demoThresholds())
	err := notify.Send(ctx, notify.Channel{ID: ch.ID, Type: ch.Type, Name: "personal", Enabled: ch.Enabled, Config: ch.Config, PublicOnly: true}, ev)
	_ = s.st.RecordUserNotifyDelivery(ctx, ch.ID, err)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": notify.Redact(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
