// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// The speed test's readings all come from one run of its collector: they are dependent items of one
// master, so each run stores them under the master value's timestamp. Joined on it they are the runs
// list under the Speed chart, and the reason a failed run shows on hover.

// runsRawMax is how far back a sensor measured by runs is read from raw history, each run at its own
// time, instead of hourly trends: the speed test keeps its history 90 days.
const runsRawMax = 90 * 24 * time.Hour

// speedRun is one speed test run. Speeds are Mbps and round trips seconds, as the template stores
// them; a reading the run didn't produce is left out.
type speedRun struct {
	T          int64    `json:"t"`
	OK         bool     `json:"ok"`
	Error      string   `json:"error,omitempty"`
	Down       *float64 `json:"down,omitempty"`
	Up         *float64 `json:"up,omitempty"`
	Latency    *float64 `json:"latency,omitempty"`
	Jitter     *float64 `json:"jitter,omitempty"`
	LoadedDown *float64 `json:"loaded_down,omitempty"`
	LoadedUp   *float64 `json:"loaded_up,omitempty"`
	Loss       *float64 `json:"loss,omitempty"`
	Site       string   `json:"site,omitempty"`
}

type speedRunsResp struct {
	Interval string     `json:"interval"` // how often it runs ("6h")
	Kind     string     `json:"kind"`     // history (each run), or trend (hourly averages, no reason or site)
	Runs     []speedRun `json:"runs"`     // newest first
}

// runKeys are the readings a run is made of.
var runKeys = map[string]bool{
	"speedtest.ok": true, "speedtest.error": true, "speedtest.down": true, "speedtest.up": true,
	"speedtest.latency": true, "speedtest.jitter": true, "speedtest.loaded.down": true,
	"speedtest.loaded.up": true, "speedtest.loss": true, "speedtest.site": true,
}

// handleSpeedtestRuns lists a host's speed test runs over a range (GET /api/hosts/{id}/speedtest/runs).
func (s *Server) handleSpeedtestRuns(w http.ResponseWriter, r *http.Request) {
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
	hostID := r.PathValue("id")
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	now := time.Now().Unix()
	from := now - int64(rng.dur.Seconds())
	raw := rng.dur <= runsRawMax
	resp := speedRunsResp{Interval: s.speedtestInterval(ctx, hostID), Kind: "history"}
	if !raw {
		resp.Kind = "trend"
	}
	vals := map[string]map[int64]string{}
	for _, it := range items {
		if !runKeys[it.Key] {
			continue
		}
		m := map[int64]string{}
		switch {
		case raw:
			pts, err := s.zbx.History(ctx, it.ItemID, atoi(it.ValueType), from, now)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
			for _, p := range pts {
				m[atoi64(p.Clock)] = p.Value
			}
		case numericValueType(it.ValueType):
			pts, err := s.zbx.Trends(ctx, it.ItemID, from, now)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
				return
			}
			for _, p := range pts {
				m[atoi64(p.Clock)] = p.ValueAvg
			}
		}
		vals[it.Key] = m
	}
	resp.Runs = joinRuns(vals)
	writeJSON(w, http.StatusOK, resp)
}

// speedtestInterval is how often the host's speed test runs: its own setting, else the template's.
func (s *Server) speedtestInterval(ctx context.Context, hostID string) string {
	const macro = "{$SPEEDTEST.INTERVAL}"
	cur := map[string]zabbix.HostMacro{}
	if hm, err := s.zbx.HostMacros(ctx, hostID); err == nil {
		for _, m := range hm {
			cur[m.Macro] = m
		}
	}
	factory, _ := provision.TemplateFactoryDefaults()
	return macroValueOr(cur, macro, factory[provision.TemplateSpeedtest][macro])
}

// joinRuns makes one run of each speedtest.ok reading (every run stores one), newest first, with the
// readings stored at the same moment (a second either way, in case a value was rounded). The test site
// is stored only when it changes, and daily, so a run carries the one in effect at the time.
func joinRuns(vals map[string]map[int64]string) []speedRun {
	oks := vals["speedtest.ok"]
	ts := make([]int64, 0, len(oks))
	for t := range oks {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	at := func(key string, t int64) (string, bool) {
		m := vals[key]
		for _, d := range []int64{0, -1, 1, -2, 2} {
			if v, ok := m[t+d]; ok {
				return v, true
			}
		}
		return "", false
	}
	num := func(key string, t int64) *float64 {
		if v, ok := at(key, t); ok {
			return pf(v)
		}
		return nil
	}
	siteTs := make([]int64, 0, len(vals["speedtest.site"]))
	for t := range vals["speedtest.site"] {
		siteTs = append(siteTs, t)
	}
	sort.Slice(siteTs, func(i, j int) bool { return siteTs[i] < siteTs[j] })
	siteAt := func(t int64) string {
		i := sort.Search(len(siteTs), func(i int) bool { return siteTs[i] > t+2 })
		if i == 0 {
			return ""
		}
		return vals["speedtest.site"][siteTs[i-1]]
	}
	runs := make([]speedRun, 0, len(ts))
	for i := len(ts) - 1; i >= 0; i-- {
		t := ts[i]
		run := speedRun{T: t, Down: num("speedtest.down", t), Up: num("speedtest.up", t),
			Latency: num("speedtest.latency", t), Jitter: num("speedtest.jitter", t),
			LoadedDown: num("speedtest.loaded.down", t), LoadedUp: num("speedtest.loaded.up", t),
			Loss: num("speedtest.loss", t), Site: siteAt(t)}
		if v := pf(oks[t]); v != nil {
			run.OK = *v >= 0.5 // an hourly average on trend ranges: a failed run pulls it under
		}
		if e, ok := at("speedtest.error", t); ok && !run.OK {
			run.Error = e
		}
		runs = append(runs, run)
	}
	return runs
}
