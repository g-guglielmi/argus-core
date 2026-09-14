package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
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

// --- Daily growth buckets for counter-total items (AdGuard queries/blocked) ---
//
// A rolling counter total is meaningless as a raw sparkline/reading; what the row should show is
// "how much per day". handleDaily serves one growth bucket per LOCAL calendar day (the viewer's
// zone, passed as a JS getTimezoneOffset value) - the same math the bar chart runs client-side.
// GET /api/daily?items=id1,id2&days=7&off=-120  ->  {"<id>": [d-6, ..., d-1, today-so-far], ...}
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
	if days < 1 || days > 31 {
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
	// Fetch one extra day of trends so the oldest displayed bucket has a previous-day baseline.
	from := today0.AddDate(0, 0, -days).Unix()

	// debug=1 returns a verbose diagnostic view of the same computation (inputs, per-day
	// first/last, buckets) instead of the compact map - for chasing bucket/baseline disputes
	// against live data. Not consumed by the app.
	debug := r.URL.Query().Get("debug") == "1"
	iso := func(ts int64) string { return time.Unix(ts, 0).In(loc).Format("2006-01-02 15:04:05") }
	diag := map[string]any{}

	out := make(map[string][]float64, len(ids))
	for _, id := range ids {
		it, err := s.zbx.Item(ctx, id)
		if err != nil || !numericValueType(it.ValueType) {
			if debug {
				diag[id] = map[string]any{"error": fmt.Sprintf("item.get failed or non-numeric (err=%v)", err)}
			}
			continue
		}
		mode := dailyMode(it.Key)
		tps, err := s.zbx.Trends(ctx, id, from, now.Unix())
		if err != nil {
			if debug {
				diag[id] = map[string]any{"key": it.Key, "error": "trend.get failed: " + err.Error()}
			}
			continue
		}
		pts := make([]dailyPt, 0, len(tps)+1)
		for _, p := range tps {
			// Counters use value_MAX: a trend row's avg is the mid-hour value, so a max-based
			// close lands on the hour's END - a rising counter's true total. A daily RATIO uses
			// the avg instead: its max would be the noisy intraday peak, while by day's end the
			// rate has converged, so the closing hour's avg ≈ the day's final rate.
			v := pf(p.ValueMax)
			if mode == "close" {
				v = pf(p.ValueAvg)
			}
			if v != nil {
				pts = append(pts, dailyPt{t: atoi64(p.Clock), v: *v})
			}
		}
		// Trends lag the open hour; the item's live last value freshens today's bucket.
		if lv := pf(it.LastValue); lv != nil {
			if lc := atoi64(it.LastClock); lc > 0 {
				pts = append(pts, dailyPt{t: lc, v: *lv})
			}
		}
		switch mode {
		case "max":
			out[id] = dailyMaxes(pts, today0, days)
		case "close":
			out[id] = dailyCloses(pts, today0, days)
		default:
			out[id] = dailyDeltas(pts, today0, days)
		}
		if debug {
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
				"trend_rows": len(tps), "per_day": perDay, "buckets_oldest_to_today": out[id],
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

// dailyMode picks how an item's series reduces to one value per day: a ".today" key is a sawtooth
// "count so far today" counter (AdGuard's own per-day stats) whose day value is its PEAK; a daily
// ratio derived from those counters (block rate) closes on its LAST reading of the day; anything
// else is treated as a rolling total and reduced to day-over-day growth.
func dailyMode(key string) string {
	base, _ := splitKey(key)
	if strings.HasSuffix(base, ".today") {
		return "max"
	}
	if base == "adguard.block_pct" {
		return "close"
	}
	return "delta"
}

// srcDayMid maps a reading's clock to the midpoint of its SOURCE day. AdGuard's stats days are
// UTC-aligned regardless of the host's timezone (its hourly buckets group by UTC day), so a
// viewer east of UTC sees the counters roll well after local midnight. Group readings by the
// source's own day and credit the whole day to the LOCAL calendar date containing its midpoint:
// between local midnight and the source's rollover, "today" stays honestly empty and yesterday's
// bar finishes growing - the same shape AdGuard's own dashboard draws.
func srcDayMid(t int64) int64 { return t/86400*86400 + 43200 }

// dailyCloses reduces a daily ratio (block rate: resets with the source's day, converges through
// the day) to one value per calendar day: the LAST reading of the day - a closed day's final
// rate, or today's current rate. A day with no readings stays 0.
func dailyCloses(pts []dailyPt, today0 time.Time, days int) []float64 {
	out := make([]float64, days)
	last := make([]int64, days)
	start := today0.AddDate(0, 0, -(days - 1))
	bounds := make([]int64, days+1)
	for i := range bounds {
		bounds[i] = start.AddDate(0, 0, i).Unix() // calendar-day arithmetic: DST-safe
	}
	for _, p := range pts {
		mid := srcDayMid(p.t)
		for i := 0; i < days; i++ {
			if mid >= bounds[i] && mid < bounds[i+1] {
				if p.t >= last[i] {
					last[i], out[i] = p.t, p.v
				}
				break
			}
		}
	}
	return out
}

// dailyMaxes reduces a "today so far" sawtooth counter (resets at local midnight, rises through
// the day) to one value per calendar day, for the `days` days ending at today0's day (oldest
// first): each bucket is the day's PEAK reading - a closed day's final total, or today's running
// total (freshened by the live last value the caller appends). A day with no readings stays 0.
func dailyMaxes(pts []dailyPt, today0 time.Time, days int) []float64 {
	out := make([]float64, days)
	start := today0.AddDate(0, 0, -(days - 1))
	bounds := make([]int64, days+1)
	for i := range bounds {
		bounds[i] = start.AddDate(0, 0, i).Unix() // calendar-day arithmetic: DST-safe
	}
	for _, p := range pts {
		mid := srcDayMid(p.t) // group by the SOURCE's (UTC-aligned) day - see srcDayMid
		for i := 0; i < days; i++ {
			if mid >= bounds[i] && mid < bounds[i+1] {
				if p.v > out[i] {
					out[i] = p.v
				}
				break
			}
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
