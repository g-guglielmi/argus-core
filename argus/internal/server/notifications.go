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
		ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled, Sites: c.Sites, MinSeverity: c.MinSeverity, Config: cfg,
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
	Config      map[string]string `json:"config"`
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
	sev := req.MinSeverity
	if sev < 2 {
		sev = 2
	} else if sev > 5 {
		sev = 5
	}
	return store.NotifyChannel{
		Type: t, Name: name, Enabled: req.Enabled, Sites: cleanSites(req.Sites), MinSeverity: sev, Config: cfg,
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
	ev.ChartPNG = renderChart(demoSeries(), dr, dg, db, "") // preview the graph too
	err = notify.Send(ctx, toNotifyChannel(*ch), ev)
	// A test counts as a delivery attempt too, so the card's health line reflects it either way.
	_ = s.st.RecordNotifyDelivery(ctx, id, err)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handleNotifySites returns the distinct Zabbix host-group names, for the channel "site" picker.
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
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, out)
}
