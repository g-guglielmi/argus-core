package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"argus/internal/auth"
)

// decodeOptional decodes a small JSON body if present, ignoring an empty/absent body.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) {
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(dst)
}

// severityState maps a Zabbix trigger severity (0..5) to Argus's OK/Warning/Error model.
// info/unclassified -> ok, warning -> warning, average/high/disaster -> error.
func severityState(sev int) string {
	switch {
	case sev >= 3:
		return "error"
	case sev == 2:
		return "warning"
	default:
		return "ok"
	}
}

type hostView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Problems int    `json:"problems"`
	Severity int    `json:"severity"` // -1 when no active problem, else 0..5
	State       string   `json:"state"`  // ok | warning | error
	Paused      bool     `json:"paused"` // disabled in Zabbix (stopped collecting)
	Hidden      bool     `json:"hidden"` // Argus-side suppression (still collecting)
	PausedUntil *int64   `json:"paused_until,omitempty"`
	HiddenUntil *int64   `json:"hidden_until,omitempty"`
	Groups      []string `json:"groups"` // host groups (drive the site tree)
}

type itemView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	LastValue string `json:"last_value"`
	Units     string `json:"units"`
	LastClock int64  `json:"last_clock"` // unix seconds, 0 if never
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Numeric     bool   `json:"numeric"` // graphable (value_type float or unsigned)
	Paused      bool   `json:"paused"`  // disabled in Zabbix (stopped collecting)
	Hidden      bool   `json:"hidden"`  // Argus-side suppression (still collecting)
	PausedUntil *int64 `json:"paused_until,omitempty"`
	HiddenUntil *int64 `json:"hidden_until,omitempty"`
	Category    string `json:"category,omitempty"` // set in curated mode
	Label       string `json:"label,omitempty"`    // friendly name in curated mode
}

// numericValueType reports whether a Zabbix value_type is graphable (0 float, 3 unsigned).
func numericValueType(vt string) bool { return vt == "0" || vt == "3" }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// untilFrom converts a duration in seconds to an absolute expiry unix time; 0/negative means
// indefinite (nil).
func untilFrom(seconds int64) *int64 {
	if seconds <= 0 {
		return nil
	}
	u := time.Now().Unix() + seconds
	return &u
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}

	// Aggregate active problems per host (best-effort: if this fails, hosts still list as ok).
	worst := map[string]int{}
	count := map[string]int{}
	if triggers, err := s.zbx.ActiveTriggers(ctx); err == nil {
		for _, t := range triggers {
			if t.Value != "1" {
				continue
			}
			sev := atoi(t.Priority)
			for _, h := range t.Hosts {
				count[h.HostID]++
				if sev > worst[h.HostID] {
					worst[h.HostID] = sev
				}
			}
		}
	}

	hideMap, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	pauseMap, _ := s.st.ActiveSuppressionMap(ctx, "pause", "host")

	out := make([]hostView, 0, len(hosts))
	for _, h := range hosts {
		groups := make([]string, 0, len(h.Groups))
		for _, g := range h.Groups {
			groups = append(groups, g.Name)
		}
		hv := hostView{ID: h.HostID, Name: h.Name, Severity: -1, State: "ok", Groups: groups}
		if n := count[h.HostID]; n > 0 {
			hv.Problems = n
			hv.Severity = worst[h.HostID]
			hv.State = severityState(worst[h.HostID])
		}
		if h.Status == "1" { // disabled in Zabbix
			hv.Paused = true
			hv.PausedUntil = pauseMap[h.HostID]
		}
		if u, ok := hideMap[h.HostID]; ok {
			hv.Hidden = true
			hv.HiddenUntil = u
		}
		out = append(out, hv)
	}
	writeJSON(w, http.StatusOK, out)
}

type problemView struct {
	EventID      string   `json:"event_id"`
	Name         string   `json:"name"`
	Severity     int      `json:"severity"`
	State        string   `json:"state"`
	Acknowledged bool     `json:"acknowledged"`
	AckUntil     *int64   `json:"ack_until,omitempty"`
	ItemIDs      []string `json:"item_ids"`
}

func (s *Server) handleHostProblems(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	problems, err := s.zbx.Problems(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	// Best-effort: map each problem's trigger to the item(s) it references for highlighting.
	tids := make([]string, 0, len(problems))
	for _, p := range problems {
		tids = append(tids, p.ObjectID)
	}
	itemsByTrigger, _ := s.zbx.TriggerItems(ctx, tids)
	acked, _ := s.st.ActiveSuppressionMap(ctx, "ack", "event") // Argus is the ack source of truth

	out := make([]problemView, 0, len(problems))
	for _, p := range problems {
		sev := atoi(p.Severity)
		ids := itemsByTrigger[p.ObjectID]
		if ids == nil {
			ids = []string{}
		}
		pv := problemView{
			EventID:  p.EventID,
			Name:     p.Name,
			Severity: sev,
			State:    severityState(sev),
			ItemIDs:  ids,
		}
		if u, ok := acked[p.EventID]; ok {
			pv.Acknowledged = true
			pv.AckUntil = u
		}
		out = append(out, pv)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/events/{id}/ack — acknowledge a problem (any signed-in user), optional duration.
// Argus records the ack (so it can expire and be undone) and mirrors it to Zabbix best-effort.
func (s *Server) handleAckEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message         string `json:"message"`
		DurationSeconds int64  `json:"duration_seconds"`
	}
	decodeOptional(w, r, &req)
	id := r.PathValue("id")
	caller, _ := auth.UserFrom(r.Context())
	var by int64
	if caller != nil {
		by = caller.ID
	}
	if err := s.ackEvent(r.Context(), id, by, req.Message, untilFrom(req.DurationSeconds)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ackEvent records an acknowledgement in Argus (source of truth) and mirrors it to Zabbix
// best-effort. Shared by the API handler and the signed one-click alert link.
func (s *Server) ackEvent(ctx context.Context, eventID string, by int64, note string, until *int64) error {
	if err := s.st.SetSuppression(ctx, "ack", "event", eventID, by, note, until); err != nil {
		return err
	}
	if s.zbx.Authenticated() {
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		_ = s.zbx.AcknowledgeEvent(c, eventID, note)
	}
	return nil
}

// DELETE /api/events/{id}/ack — un-acknowledge (bring the problem back).
func (s *Server) handleUnackEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.st.ClearSuppression(r.Context(), "ack", "event", id)
	if s.zbx.Authenticated() {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		_ = s.zbx.UnacknowledgeEvent(ctx, id) // best-effort mirror
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// hideHandler hides (Argus-side suppression) a host or item; collection continues. Optional duration.
func (s *Server) hideHandler(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Note            string `json:"note"`
			DurationSeconds int64  `json:"duration_seconds"`
		}
		decodeOptional(w, r, &req)
		caller, _ := auth.UserFrom(r.Context())
		var by int64
		if caller != nil {
			by = caller.ID
		}
		if err := s.st.SetSuppression(r.Context(), "hide", scope, r.PathValue("id"), by, req.Note, untilFrom(req.DurationSeconds)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// unhideHandler un-hides a host or item.
func (s *Server) unhideHandler(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.st.ClearSuppression(r.Context(), "hide", scope, r.PathValue("id")); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// zbxEnableHandler pauses (enabled=false) or resumes (enabled=true) a host/item by
// disabling/enabling it in Zabbix, which actually stops/starts collection. Pause takes an
// optional duration; a background sweeper re-enables it when the expiry passes.
func (s *Server) zbxEnableHandler(scope string, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.zbx.Authenticated() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
			return
		}
		var req struct {
			DurationSeconds int64 `json:"duration_seconds"`
		}
		decodeOptional(w, r, &req)
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		id := r.PathValue("id")
		var err error
		if scope == "host" {
			err = s.zbx.SetHostEnabled(ctx, id, enabled)
		} else {
			err = s.zbx.SetItemEnabled(ctx, id, enabled)
		}
		if err != nil {
			// Most often a permission error: the token's Zabbix user lacks write access.
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
		caller, _ := auth.UserFrom(r.Context())
		var by int64
		if caller != nil {
			by = caller.ID
		}
		if enabled {
			_ = s.st.ClearSuppression(r.Context(), "pause", scope, id)
		} else {
			_ = s.st.SetSuppression(r.Context(), "pause", scope, id, by, "", untilFrom(req.DurationSeconds))
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func (s *Server) handleHostItems(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	items, err := s.zbx.Items(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	all := r.URL.Query().Get("all") == "1"
	hideMap, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	pauseMap, _ := s.st.ActiveSuppressionMap(ctx, "pause", "item")
	out := make([]itemView, 0, len(items))
	for _, it := range items {
		var clock int64
		if it.LastClock != "" {
			clock, _ = strconv.ParseInt(it.LastClock, 10, 64)
		}
		iv := itemView{
			ID:        it.ItemID,
			Name:      it.Name,
			Key:       it.Key,
			LastValue: it.LastValue,
			Units:     it.Units,
			LastClock: clock,
			Supported: it.State == "0",
			Numeric:   numericValueType(it.ValueType),
			Paused:    it.Status == "1", // disabled in Zabbix
		}
		if iv.Paused {
			iv.PausedUntil = pauseMap[it.ItemID]
		}
		if u, ok := hideMap[it.ItemID]; ok {
			iv.Hidden = true
			iv.HiddenUntil = u
		}
		if !all {
			cat, label, ok := classifyItem(it.Key, it.Name)
			if !ok {
				continue // hide un-curated "noise" in the default view
			}
			iv.Category, iv.Label = cat, label
		}
		out = append(out, iv)
	}
	if !all {
		sort.SliceStable(out, func(i, j int) bool {
			if ci, cj := categoryOrder[out[i].Category], categoryOrder[out[j].Category]; ci != cj {
				return ci < cj
			}
			return out[i].Label < out[j].Label
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// timeRange maps a UI range key to a lookback window and whether to read trends vs history.
// Short ranges use raw history (30-day retention); long ranges use trends (730-day retention).
type timeRange struct {
	dur   time.Duration
	trend bool
}

var timeRanges = map[string]timeRange{
	"2h": {2 * time.Hour, false},
	"2d": {48 * time.Hour, false},
	"1M": {30 * 24 * time.Hour, true},
	"3M": {90 * 24 * time.Hour, true},
	"6M": {180 * 24 * time.Hour, true},
	"1Y": {365 * 24 * time.Hour, true},
}

type seriesPoint struct {
	T   int64    `json:"t"`
	V   *float64 `json:"v,omitempty"`
	Min *float64 `json:"min,omitempty"`
	Avg *float64 `json:"avg,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

type seriesResp struct {
	Name   string        `json:"name"`
	Units  string        `json:"units"`
	Kind   string        `json:"kind"` // history | trend
	Points []seriesPoint `json:"points"`
}

func pf(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (s *Server) handleItemHistory(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	rng, ok := timeRanges[r.URL.Query().Get("range")]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid range"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	itemID := r.PathValue("id")
	item, err := s.zbx.Item(ctx, itemID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	if !numericValueType(item.ValueType) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this sensor is not numeric and has no graph"})
		return
	}

	now := time.Now().Unix()
	from := now - int64(rng.dur.Seconds())
	resp := seriesResp{Name: item.Name, Units: item.Units, Points: []seriesPoint{}}

	if rng.trend {
		resp.Kind = "trend"
		pts, err := s.zbx.Trends(ctx, itemID, from, now)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
		for _, p := range pts {
			resp.Points = append(resp.Points, seriesPoint{T: atoi64(p.Clock), Min: pf(p.ValueMin), Avg: pf(p.ValueAvg), Max: pf(p.ValueMax)})
		}
	} else {
		resp.Kind = "history"
		pts, err := s.zbx.History(ctx, itemID, atoi(item.ValueType), from, now)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
			return
		}
		for _, p := range pts {
			resp.Points = append(resp.Points, seriesPoint{T: atoi64(p.Clock), V: pf(p.Value)})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
