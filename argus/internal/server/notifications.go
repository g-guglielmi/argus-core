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
	ID      int64             `json:"id"`
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Enabled bool              `json:"enabled"`
	Site    string            `json:"site"`
	Config  map[string]string `json:"config"`
}

func toChannelView(c store.NotifyChannel) channelView {
	cfg := c.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	return channelView{ID: c.ID, Type: c.Type, Name: c.Name, Enabled: c.Enabled, Site: c.Site, Config: cfg}
}

var validChannelTypes = map[string]bool{"discord": true, "telegram": true, "email": true}

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
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Enabled bool              `json:"enabled"`
	Site    string            `json:"site"`
	Config  map[string]string `json:"config"`
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
	return store.NotifyChannel{
		Type: t, Name: name, Enabled: req.Enabled, Site: strings.TrimSpace(req.Site), Config: cfg,
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
	ev := notify.SampleEvent(time.Now().In(s.loc), s.cfg.PublicURL)
	dr, dg, db := statusRGB(ev.State)
	ev.ChartPNG = renderChart(demoSeries(), dr, dg, db) // preview the graph too
	if err := notify.Send(ctx, toNotifyChannel(*ch), ev); err != nil {
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
