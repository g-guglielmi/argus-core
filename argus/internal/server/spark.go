// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"argus/internal/zabbix"
)

// handleSpark returns a compact recent series (down to ~24 values) per requested item, for the
// inline sparklines. One item.get (value types) + up to two history.get (float + unsigned).
// GET /api/spark?items=id1,id2,...&range=2h
func (s *Server) handleSpark(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	idsParam := r.URL.Query().Get("items")
	if strings.TrimSpace(idsParam) == "" {
		writeJSON(w, http.StatusOK, map[string][]float64{})
		return
	}
	ids := strings.Split(idsParam, ",")
	if len(ids) > 300 { // safety cap
		ids = ids[:300]
	}
	rng, ok := timeRanges[r.URL.Query().Get("range")]
	if !ok {
		rng = timeRanges["2h"]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	types, err := s.zbx.ItemValueTypes(ctx, ids)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	byType := map[int][]string{}
	for id, vt := range types {
		if vt == "0" || vt == "3" { // only numeric items have a sparkline
			byType[atoi(vt)] = append(byType[atoi(vt)], id)
		}
	}
	from := time.Now().Unix() - int64(rng.dur.Seconds())
	series := map[string][]float64{}
	for vt, group := range byType {
		pts, err := s.zbx.HistoryMulti(ctx, group, vt, from)
		if err != nil {
			continue
		}
		for _, p := range pts {
			if v := pf(p.Value); v != nil {
				series[p.ItemID] = append(series[p.ItemID], *v)
			}
		}
	}
	out := make(map[string][]float64, len(series))
	for id, vals := range series {
		out[id] = downsample(vals, 24)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Daily buckets for daily-resetting sensors (AdGuard queries/blocked/block rate) ---
//
// handleDaily is the single source of daily buckets: the row headline, the mini bars, AND the
// big bar charts all consume it, so they can never disagree. One value per LOCAL calendar day
// (the viewer's zone, passed as a JS getTimezoneOffset value), oldest first, last = today so far.
// GET /api/daily?items=id1,id2&days=7&off=-120  ->  {"<id>": [d-6, ..., d-1, today], ...}
func (s *Server) handleDaily(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	idsParam := strings.TrimSpace(r.URL.Query().Get("items"))
	if idsParam == "" {
		writeJSON(w, http.StatusOK, map[string][]float64{})
		return
	}
	ids := strings.Split(idsParam, ",")
	if len(ids) > 50 { // a host has a handful of counter items; cap defensively
		ids = ids[:50]
	}
	days := atoi(r.URL.Query().Get("days"))
	if days < 1 || days > 366 {
		days = 7
	}
	// off is JS Date.getTimezoneOffset(): minutes such that UTC = local + off (UTC+2 -> -120).
	off := atoi(r.URL.Query().Get("off"))
	if off < -14*60 || off > 14*60 {
		off = 0
	}
	loc := time.FixedZone("viewer", -off*60)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	now := time.Now()
	y, mo, d := now.In(loc).Date()
	today0 := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	// Fetch one extra day of trends so the oldest displayed bucket has its midnight baseline.
	from := today0.AddDate(0, 0, -days).Unix()

	// debug=1 returns a verbose diagnostic view of the same computation (inputs, per-day
	// first/last, buckets) instead of the compact map - for chasing bucket disputes against
	// live data. Not consumed by the app.
	debug := r.URL.Query().Get("debug") == "1"
	iso := func(ts int64) string { return time.Unix(ts, 0).In(loc).Format("2006-01-02 15:04:05") }
	diag := map[string]any{}

	// loadPts assembles an item's readings: hourly trend MAXes (the hour-END value of a counter
	// that only rises within its source day) plus the live last value (trends lag the open hour).
	loadPts := func(id string) (*zabbix.Item, []dailyPt, string) {
		it, err := s.zbx.Item(ctx, id)
		if err != nil || !numericValueType(it.ValueType) {
			return nil, nil, fmt.Sprintf("item.get failed or non-numeric (err=%v)", err)
		}
		tps, err := s.zbx.Trends(ctx, id, from, now.Unix())
		if err != nil {
			return it, nil, "trend.get failed: " + err.Error()
		}
		pts := make([]dailyPt, 0, len(tps)+1)
		for _, p := range tps {
			if v := pf(p.ValueMax); v != nil {
				pts = append(pts, dailyPt{t: atoi64(p.Clock), v: *v})
			}
		}
		if lv := pf(it.LastValue); lv != nil {
			if lc := atoi64(it.LastClock); lc > 0 {
				pts = append(pts, dailyPt{t: lc, v: *lv})
			}
		}
		return it, pts, ""
	}
	// counterBuckets memoizes the reconstructed local-day counts per item, so a batch that holds
	// queries + blocked + the block rate loads each counter once.
	counterCache := map[string][]float64{}
	counterBuckets := func(id string) []float64 {
		if b, ok := counterCache[id]; ok {
			return b
		}
		_, pts, errMsg := loadPts(id)
		if errMsg != "" {
			return nil
		}
		b := dailySplitDeltas(pts, today0, days, from)
		counterCache[id] = b
		return b
	}

	out := make(map[string][]float64, len(ids))
	for _, id := range ids {
		it, err := s.zbx.Item(ctx, id)
		if err != nil {
			if debug {
				diag[id] = map[string]any{"error": "item.get failed: " + err.Error()}
			}
			continue
		}
		mode := dailyMode(it.Key)
		switch mode {
		case "max":
			if b := counterBuckets(id); b != nil {
				out[id] = b
			}
		case "rate":
			// The block rate's per-day value derives from its sibling counters on the same host
			// (blocked/total per LOCAL day) - its own series can't be reconstructed into local
			// days (a ratio isn't monotonic), and this keeps the whole DNS section on one math.
			items, ierr := s.zbx.Items(ctx, it.HostID)
			if ierr != nil {
				if debug {
					diag[id] = map[string]any{"key": it.Key, "mode": mode, "error": "item list failed: " + ierr.Error()}
				}
				continue
			}
			var qid, bid string
			for _, hi := range items {
				if base, _ := splitKey(hi.Key); base == "adguard.queries.today" {
					qid = hi.ItemID
				} else if base == "adguard.blocked.today" {
					bid = hi.ItemID
				}
			}
			q, b := counterBuckets(qid), counterBuckets(bid)
			out[id] = rateBuckets(q, b)
			if debug {
				diag[id] = map[string]any{"key": it.Key, "mode": mode, "queries_item": qid, "blocked_item": bid,
					"queries_buckets": q, "blocked_buckets": b, "buckets_oldest_to_today": out[id]}
			}
			continue
		default:
			_, pts, errMsg := loadPts(id)
			if errMsg != "" {
				if debug {
					diag[id] = map[string]any{"key": it.Key, "mode": mode, "error": errMsg}
				}
				continue
			}
			out[id] = dailyDeltas(pts, today0, days)
		}
		if debug {
			_, pts, _ := loadPts(id)
			sort.Slice(pts, func(i, j int) bool { return pts[i].t < pts[j].t })
			perDay := []map[string]any{}
			var cur map[string]any
			var curDay string
			for _, p := range pts {
				dk := iso(p.t)[:10]
				if dk != curDay {
					cur = map[string]any{"day": dk, "first_t": iso(p.t), "first_v": p.v, "last_t": iso(p.t), "last_v": p.v, "points": 1}
					perDay = append(perDay, cur)
					curDay = dk
				} else {
					cur["last_t"], cur["last_v"] = iso(p.t), p.v
					cur["points"] = cur["points"].(int) + 1
				}
			}
			diag[id] = map[string]any{
				"key": it.Key, "mode": mode, "value_type": it.ValueType,
				"lastvalue": it.LastValue, "lastclock": iso(atoi64(it.LastClock)),
				"per_day": perDay, "buckets_oldest_to_today": out[id],
			}
		}
	}
	if debug {
		writeJSON(w, http.StatusOK, map[string]any{
			"now": iso(now.Unix()), "off_minutes": off, "today0": iso(today0.Unix()),
			"window_from": iso(from), "days": days, "items": diag,
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type dailyPt struct {
	t int64
	v float64
}

// dailyDeltas reduces a counter-total series to one growth value per calendar day, for the `days`
// days ending at today0's day (oldest first, last = today-so-far). A day's growth is its last
// reading minus the previous day's last reading, clamped at 0 (rolling-window totals dip when the
// window slides or the stats reset - a dip is "no growth", not negative traffic). When the previous
// day has no reading (a fresh item, or a gap), the day's own FIRST reading is the baseline, so a
// brand-new device still gets a today bar. A day with no readings at all stays 0.
func dailyDeltas(pts []dailyPt, today0 time.Time, days int) []float64 {
	sort.Slice(pts, func(i, j int) bool { return pts[i].t < pts[j].t })
	// First/last reading per day offset (0 = the oldest fetched day, days = today).
	type fl struct {
		has         bool
		first, last float64
	}
	byDay := make([]fl, days+1)
	start := today0.AddDate(0, 0, -days)
	bounds := make([]int64, days+2)
	for i := range bounds {
		bounds[i] = start.AddDate(0, 0, i).Unix() // calendar-day arithmetic: DST-safe
	}
	for _, p := range pts {
		for i := 0; i <= days; i++ {
			if p.t >= bounds[i] && p.t < bounds[i+1] {
				if !byDay[i].has {
					byDay[i] = fl{has: true, first: p.v, last: p.v}
				} else {
					byDay[i].last = p.v // pts are clock-sorted
				}
				break
			}
		}
	}
	out := make([]float64, days)
	for i := 1; i <= days; i++ {
		cur := byDay[i]
		if !cur.has {
			continue // no readings that day -> 0
		}
		base := cur.first
		if prev := byDay[i-1]; prev.has {
			base = prev.last
		}
		if d := cur.last - base; d > 0 {
			out[i-1] = d
		}
	}
	return out
}

// dailyMode picks how an item reduces to one value per day: a ".today" key is a sawtooth "count
// so far today" counter (AdGuard's own per-day stats) reconstructed into TRUE local days; the
// block rate derives per day from its sibling counters; anything else is treated as a rolling
// total and reduced to day-over-day growth.
func dailyMode(key string) string {
	base, _ := splitKey(key)
	if strings.HasSuffix(base, ".today") {
		return "max"
	}
	if base == "adguard.block_pct" {
		return "rate"
	}
	return "delta"
}

// dailySplitDeltas reconstructs TRUE local-calendar-day counts from a "today so far" counter
// whose SOURCE day is misaligned with the viewer's - AdGuard buckets its stats at hard-coded UTC
// midnights (AdGuardHome#4560), so for a viewer east of UTC the counter rolls after local
// midnight. Within a source day the counter only grows, so the growth between two consecutive
// readings is EXACT and is credited to the local day of the later reading; a drop means the
// source day rolled over between the samples, and the new reading's value is the growth since
// that reset. Readings are hour-end trend maxes, so for whole-hour timezone offsets the viewer's
// midnight falls exactly on a reading and days split precisely. The window's first reading has
// no baseline: if the series starts well inside the window (a freshly monitored item, not the
// window edge of an old one) its carried value is credited so day one isn't undercounted.
func dailySplitDeltas(pts []dailyPt, today0 time.Time, days int, windowFrom int64) []float64 {
	sort.Slice(pts, func(i, j int) bool { return pts[i].t < pts[j].t })
	out := make([]float64, days)
	start := today0.AddDate(0, 0, -(days - 1))
	bounds := make([]int64, days+1)
	for i := range bounds {
		bounds[i] = start.AddDate(0, 0, i).Unix() // calendar-day arithmetic: DST-safe
	}
	credit := func(t int64, v float64) {
		if v <= 0 {
			return
		}
		i := sort.Search(len(bounds), func(k int) bool { return bounds[k] > t }) - 1
		if i >= 0 && i < days {
			out[i] += v
		}
	}
	for k, p := range pts {
		if k == 0 {
			if p.t > windowFrom+2*3600 {
				credit(p.t, p.v)
			}
			continue
		}
		d := p.v - pts[k-1].v
		if d < 0 {
			d = p.v // source-day rollover between the samples: the new count started at 0
		}
		credit(p.t, d)
	}
	return out
}

// rateBuckets derives per-day block rates from the per-day query/blocked counts: the same local
// days, the same math the headline shows (blocked as a share of that day's total, one decimal).
func rateBuckets(total, blocked []float64) []float64 {
	out := make([]float64, len(total))
	for i := range total {
		if total[i] > 0 && i < len(blocked) {
			b := blocked[i]
			if b > total[i] {
				b = total[i]
			}
			out[i] = float64(int(b/total[i]*1000+0.5)) / 10
		}
	}
	return out
}

// downsample reduces a series to at most n points, keeping the first and last.
func downsample(vals []float64, n int) []float64 {
	if len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = vals[i*(len(vals)-1)/(n-1)]
	}
	return out
}
