// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"argus/internal/zabbix"
)

// Uptime targets (DESIGN section 7b): a guaranteed uptime per calendar month (99.9 %), set on a site for
// its hosts or on a host. The month's downtime is measured like the host's uptime (its master sensor,
// else its ping), from the hourly trends read exactly where an hour wasn't all up; time in a
// maintenance window counts as up (planned work isn't held against the target). The allowance is the
// downtime the target leaves in the whole month; "Downtime allowance used" is the share of it spent,
// a sensor through the push channel (a warning from {$UPTIME.ALLOWANCE.WARN} %, an error from
// {$UPTIME.ALLOWANCE.HIGH} %), so a host about to miss its target alerts before it does.

// targetView is a host's target this month and how much of its allowance is spent.
type targetView struct {
	Pct      float64 `json:"pct"`
	Source   string  `json:"source"` // host | site
	Site     string  `json:"site,omitempty"`
	Month    string  `json:"month"`     // "2026-10"
	Allowed  int64   `json:"allowed_s"` // downtime the target allows in the month
	Used     int64   `json:"used_s"`    // downtime so far, outside maintenance windows
	UsedPct  float64 `json:"used_pct"`  // Used / Allowed * 100
	Elapsed  int64   `json:"elapsed_s"`
	UpPct    float64 `json:"uptime_pct"` // the month so far
	Measured string  `json:"measured,omitempty"`
}

// effectiveTarget is a host's target: its own (0 = none), else its site's.
func effectiveTarget(sites, hosts map[string]float64, hostID string, groups []string) (float64, string, string) {
	if p, ok := hosts[hostID]; ok {
		return p, "host", ""
	}
	site := siteOf(groups)
	if p, ok := sites[site]; ok && p > 0 {
		return p, "site", site
	}
	return 0, "", ""
}

// monthDowntime is a sensor's downtime from from to now outside the given spans, from hourly trends
// (an hour all up costs nothing; one partly down costs its share, spread evenly) and the raw checks
// for the hours the trends don't cover yet.
func monthDowntime(trends []zabbix.TrendRow, hist []zabbix.HistoryPoint, spans [][2]int64, from, now int64) float64 {
	outside := func(a, b int64) float64 { // seconds of [a,b) outside the spans
		total := float64(b - a)
		for _, s := range spans {
			lo, hi := max(a, s[0]), min(b, s[1])
			if hi > lo {
				total -= float64(hi - lo)
			}
		}
		return math.Max(0, total)
	}
	down := 0.0
	var lastHour int64 = from - 3600
	for _, r := range trends {
		c := atoi64(r.Clock)
		if c < from-3599 {
			continue
		}
		if c > lastHour {
			lastHour = c
		}
		avg, err := strconv.ParseFloat(r.ValueAvg, 64)
		if err != nil || avg >= 1 {
			continue
		}
		a, b := max(c, from), min(c+3600, now)
		if b <= a {
			continue
		}
		down += (1 - math.Max(0, avg)) * outside(a, b)
	}
	// The hours after the last trend: each raw check stands for the time to the next one.
	tail := lastHour + 3600
	var pts [][2]int64 // t, up
	for _, h := range hist {
		t := atoi64(h.Clock)
		if t < tail {
			continue
		}
		v, err := strconv.ParseFloat(h.Value, 64)
		if err != nil {
			continue
		}
		up := int64(0)
		if v >= 0.5 {
			up = 1
		}
		pts = append(pts, [2]int64{t, up})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
	for i, p := range pts {
		if p[1] == 1 {
			continue
		}
		end := now
		if i+1 < len(pts) {
			end = pts[i+1][0]
		}
		if end-p[0] > 600 { // a check stands for at most 10 minutes
			end = p[0] + 600
		}
		down += outside(p[0], end)
	}
	return down
}

var targetCache = struct {
	sync.Mutex
	m map[string]targetEntry
}{m: map[string]targetEntry{}}

type targetEntry struct {
	at time.Time
	v  *targetView
}

// hostTarget is a host's target this month (nil without one), kept 5 minutes.
func (s *Server) hostTarget(ctx context.Context, hostID string) *targetView {
	targetCache.Lock()
	if e, ok := targetCache.m[hostID]; ok && time.Since(e.at) < 5*time.Minute {
		targetCache.Unlock()
		return e.v
	}
	targetCache.Unlock()
	v := s.computeTarget(ctx, hostID)
	targetCache.Lock()
	targetCache.m[hostID] = targetEntry{at: time.Now(), v: v}
	targetCache.Unlock()
	return v
}

func forgetTargets() {
	targetCache.Lock()
	targetCache.m = map[string]targetEntry{}
	targetCache.Unlock()
}

func (s *Server) computeTarget(ctx context.Context, hostID string) *targetView {
	if s.zbx == nil || !s.zbx.Authenticated() {
		return nil
	}
	sites, hosts, err := s.st.UptimeTargets(ctx)
	if err != nil || (len(sites) == 0 && len(hosts) == 0) {
		return nil
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return nil
	}
	groups := idx[hostID].Groups
	pct, source, site := effectiveTarget(sites, hosts, hostID, groups)
	if pct <= 0 {
		return nil
	}
	itemID := s.hostUptimeItems(ctx, []string{hostID})[hostID]
	if itemID == "" {
		return nil
	}
	items, err := s.zbx.ItemsByIDs(ctx, []string{itemID})
	if err != nil {
		return nil
	}
	it := items[itemID]
	loc := s.location()
	now := time.Now()
	lt := now.In(loc)
	monthStart := time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, loc)
	monthEnd := monthStart.AddDate(0, 1, 0)
	trends, err := s.zbx.TrendRows(ctx, []string{itemID}, monthStart.Unix(), now.Unix())
	if err != nil {
		return nil
	}
	trends, _ = s.exactUptime(ctx, trends, map[string]string{itemID: it.ValueType})
	hist, _ := s.zbx.History(ctx, itemID, atoi(it.ValueType), now.Add(-3*time.Hour).Unix(), now.Unix())
	var spans [][2]int64
	if windows, err := s.st.MaintenanceWindows(ctx); err == nil {
		for _, w := range windows {
			if w.Enabled && windowCovers(w, hostID, groups) {
				for _, sp := range occurrencesBetween(w, monthStart, now, loc) {
					spans = append(spans, [2]int64{sp.From.Unix(), sp.To.Unix()})
				}
			}
		}
	}
	down := monthDowntime(trends, hist, spans, monthStart.Unix(), now.Unix())
	allowed := (100 - pct) / 100 * float64(monthEnd.Unix()-monthStart.Unix())
	elapsed := now.Unix() - monthStart.Unix()
	v := &targetView{Pct: pct, Source: source, Site: site, Month: monthStart.Format("2006-01"), Allowed: int64(allowed), Used: int64(down), Elapsed: elapsed,
		Measured: sensorLabel(it.Key, it.Name)}
	if allowed > 0 {
		v.UsedPct = math.Round(down/allowed*1000) / 10
	}
	if elapsed > 0 {
		v.UpPct = math.Floor((1-down/float64(elapsed))*100000) / 1000 // never rounds up to 100
	}
	return v
}

// targetedHosts is every host with a target now (its own, or its site's), for the push channel.
func (s *Server) targetedHosts(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	sites, hosts, err := s.st.UptimeTargets(ctx)
	if err != nil || (len(sites) == 0 && len(hosts) == 0) {
		return out
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return out
	}
	for h, hi := range idx {
		if p, _, _ := effectiveTarget(sites, hosts, h, hi.Groups); p > 0 {
			out[h] = true
		}
	}
	return out
}

// pushTargetEntry is a host's target as its template reads it.
type pushTargetEntry struct {
	Pct     float64 `json:"pct"`
	UsedPct float64 `json:"used_pct"`
}

// GET /api/hosts/{id}/uptime-target: the host's own target (null: it follows its site), its site's.
func (s *Server) handleHostUptimeTarget(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	sites, hosts, err := s.st.UptimeTargets(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	out := map[string]any{"host": nil}
	if p, ok := hosts[hostID]; ok {
		out["host"] = p
	}
	if idx, err := s.hostIndex(r.Context()); err == nil {
		site := siteOf(idx[hostID].Groups)
		out["site"] = site
		if p, ok := sites[site]; ok {
			out["site_pct"] = p
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// readTargetPct reads {pct: number|null}: null clears it; 0 is none; else 90 to under 100.
func readTargetPct(w http.ResponseWriter, r *http.Request) (pct float64, clear, ok bool) {
	var req struct {
		Pct *float64 `json:"pct"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return 0, false, false
	}
	if req.Pct == nil {
		return 0, true, true
	}
	p := *req.Pct
	if p != 0 && (p < 90 || p >= 100 || math.IsNaN(p)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a target is between 90 % and under 100 %"})
		return 0, false, false
	}
	return math.Round(p*1000) / 1000, false, true
}

func pctText(p float64, clear bool, none string) string {
	if clear {
		return none
	}
	if p == 0 {
		return "none"
	}
	return strconv.FormatFloat(p, 'f', -1, 64) + " %"
}

// PUT /api/hosts/{id}/uptime-target (admin, helpdesk): {pct}: null follows the site, 0 none.
func (s *Server) handleSetHostUptimeTarget(w http.ResponseWriter, r *http.Request) {
	pct, clear, ok := readTargetPct(w, r)
	if !ok {
		return
	}
	hostID := r.PathValue("id")
	_, hosts, _ := s.st.UptimeTargets(r.Context())
	was, had := hosts[hostID]
	if err := s.st.SetUptimeTarget(r.Context(), "host", hostID, pct, clear); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	changeDiff(r, "Uptime target", pctText(was, !had, "its site's"), pctText(pct, clear, "its site's"))
	forgetTargets()
	s.wakeDomainChecks() // the push template follows the hosts that need it
	s.handleHostUptimeTarget(w, r)
}

// GET /api/sites/{site}/uptime-target: the site's target (null: none).
func (s *Server) handleSiteUptimeTarget(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	sites, _, err := s.st.UptimeTargets(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	out := map[string]any{"site": site, "pct": nil}
	if p, ok := sites[site]; ok {
		out["pct"] = p
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/sites/{site}/uptime-target (admin, helpdesk within their sites): {pct}: null or 0 removes it.
func (s *Server) handleSetSiteUptimeTarget(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	if !scopeFrom(r).coversGroup(site) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "site not found"})
		return
	}
	pct, clear, ok := readTargetPct(w, r)
	if !ok {
		return
	}
	if pct == 0 {
		clear = true
	}
	sites, _, _ := s.st.UptimeTargets(r.Context())
	was, had := sites[site]
	if err := s.st.SetUptimeTarget(r.Context(), "site", site, pct, clear); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	changeObject(r, site)
	changeDiff(r, "Uptime target", pctText(was, !had, "none"), pctText(pct, clear, "none"))
	forgetTargets()
	s.wakeDomainChecks()
	out := map[string]any{"site": site, "pct": nil}
	if !clear {
		out["pct"] = pct
	}
	writeJSON(w, http.StatusOK, out)
}
