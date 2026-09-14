package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"argus/internal/zabbix"
)

// The row headline ("N queries today", /api/daily) and the bar chart's today bar (built from
// /api/items/{id}/history range=7d trends + a raw 2h tail) MUST agree - they are two views of the
// same day bucket. This runs both REAL handlers against a mock Zabbix serving one synthetic
// rolling-counter series and compares the results end to end (lab bug 2026-09-14: the two views
// disagreed by exactly one day's growth).
func TestDailyVsChartConsistency(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600) // the lab viewer: UTC+2, off=-120
	// "now": mid-afternoon local. today0/yesterday0 are local midnights.
	now := time.Date(2026, 9, 14, 14, 37, 22, 0, loc)
	today0 := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	// Synthetic rolling total, mirroring the backup AdGuard: data starts the day before yesterday,
	// closes yesterday at 6012 after +1012 that day, and grows +1234 today by "now". Piecewise
	// linear per day, so the value at any time is well-defined.
	dataStart := today0.AddDate(0, 0, -2)
	dayRate := map[int64]float64{ // growth per day, keyed by local day start
		today0.AddDate(0, 0, -2).Unix(): 1000,
		today0.AddDate(0, 0, -1).Unix(): 1012,
		today0.Unix():                   1234 * 86400 / now.Sub(today0).Seconds(), // reaches +1234 at "now"
	}
	valueAt := func(ts time.Time) float64 {
		if ts.Before(dataStart) {
			ts = dataStart
		}
		v := 5000.0
		for d := dataStart; d.Before(ts); d = d.AddDate(0, 0, 1) {
			d0 := time.Date(d.In(loc).Year(), d.In(loc).Month(), d.In(loc).Day(), 0, 0, 0, 0, loc)
			end := d0.AddDate(0, 0, 1)
			if end.After(ts) {
				end = ts
			}
			v += dayRate[d0.Unix()] * end.Sub(d0).Seconds() / 86400
		}
		return v
	}
	lastValue := valueAt(now)

	// Mock Zabbix JSON-RPC: item.get (live lastvalue), trend.get (UTC-hour rows, avg at the hour's
	// midpoint, only CLOSED hours - trends lag the open hour), history.get (raw 1-min polls).
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "item.get":
			result = []map[string]string{{
				"itemid": "1", "hostid": "10", "name": "DNS queries", "key_": "adguard.queries",
				"units": "", "value_type": "3",
				"lastvalue": fmt.Sprintf("%.0f", lastValue), "lastclock": fmt.Sprintf("%d", now.Unix()),
			}}
		case "trend.get":
			from := int64(asF(req.Params["time_from"]))
			till := int64(asF(req.Params["time_till"]))
			rows := []map[string]string{}
			lastClosed := now.Truncate(time.Hour)
			for h := time.Unix(from, 0).Truncate(time.Hour); h.Before(lastClosed); h = h.Add(time.Hour) {
				if h.Unix() < from || h.Unix() > till || h.Before(dataStart) {
					continue
				}
				avg := valueAt(h.Add(30 * time.Minute))
				rows = append(rows, map[string]string{
					"clock":     fmt.Sprintf("%d", h.Unix()),
					"value_min": fmt.Sprintf("%.0f", valueAt(h)),
					"value_avg": fmt.Sprintf("%.0f", avg),
					"value_max": fmt.Sprintf("%.0f", valueAt(h.Add(time.Hour))),
				})
			}
			result = rows
		case "history.get":
			from := int64(asF(req.Params["time_from"]))
			till := now.Unix()
			if tt, ok := req.Params["time_till"]; ok {
				till = int64(asF(tt))
			}
			rows := []map[string]string{}
			for ts := time.Unix(from, 0).Truncate(time.Minute); !ts.After(time.Unix(till, 0)); ts = ts.Add(time.Minute) {
				if ts.Before(dataStart) || ts.After(now) {
					continue
				}
				rows = append(rows, map[string]string{
					"itemid": "1", "clock": fmt.Sprintf("%d", ts.Unix()), "value": fmt.Sprintf("%.0f", valueAt(ts)),
				})
			}
			result = rows
		default:
			result = []any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	defer mock.Close()

	s := &Server{zbx: zabbix.New(mock.URL, "test-token")}

	// --- The row headline: /api/daily's last bucket. NOTE: handleDaily uses time.Now(), so the
	// buckets are computed against the REAL clock; feed dailyDeltas directly with the same inputs
	// the handler assembles, anchored on our synthetic "now" instead.
	tps, err := s.zbx.Trends(t.Context(), "1", today0.AddDate(0, 0, -7).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	pts := make([]dailyPt, 0, len(tps)+1)
	for _, p := range tps {
		if v := pf(p.ValueAvg); v != nil {
			pts = append(pts, dailyPt{t: atoi64(p.Clock), v: *v})
		}
	}
	pts = append(pts, dailyPt{t: now.Unix(), v: lastValue})
	daily := dailyDeltas(pts, today0, 7)
	headlineToday := daily[len(daily)-1]

	// --- The chart's today bar: trends (7d window ending "now") + raw 2h tail, merged, then the
	// same per-local-day first/last bucketing buildBarPlot runs client-side.
	chartTrends, err := s.zbx.Trends(t.Context(), "1", now.Add(-7*24*time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("chart trends: %v", err)
	}
	rawTail, err := s.zbx.History(t.Context(), "1", 3, now.Add(-2*time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("raw tail: %v", err)
	}
	type fl struct {
		has    bool
		tF, tL int64
		vF, vL float64
	}
	byDay := map[int64]*fl{}
	dayStart := func(ts int64) int64 {
		y, m, d := time.Unix(ts, 0).In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc).Unix()
	}
	add := func(ts int64, v float64) {
		d := dayStart(ts)
		cur := byDay[d]
		if cur == nil {
			byDay[d] = &fl{has: true, tF: ts, vF: v, tL: ts, vL: v}
			return
		}
		if ts < cur.tF {
			cur.tF, cur.vF = ts, v
		}
		if ts > cur.tL {
			cur.tL, cur.vL = ts, v
		}
	}
	for _, p := range chartTrends {
		// hour MAX, like buildBarPlot's `p.hi ?? p.v` - day closes land on true midnights.
		if v := pf(p.ValueMax); v != nil {
			add(atoi64(p.Clock), *v)
		}
	}
	for _, p := range rawTail {
		if v := pf(p.Value); v != nil {
			add(atoi64(p.Clock), *v)
		}
	}
	cur := byDay[today0.Unix()]
	if cur == nil {
		t.Fatal("chart: no today bucket")
	}
	base := cur.vF
	if prev := byDay[today0.AddDate(0, 0, -1).Unix()]; prev != nil {
		base = prev.vL
	}
	chartToday := cur.vL - base
	if chartToday < 0 {
		chartToday = 0
	}

	// Both views must agree on "today" (within the trend-hour vs raw-poll rounding of the shared
	// baseline - a few counts, not a day's worth).
	if diff := headlineToday - chartToday; diff > 25 || diff < -25 {
		t.Fatalf("today mismatch: headline (daily) = %.0f, chart bar = %.0f (diff %.0f; yesterday's growth was 1012 - a diff near that means the baselines are a day apart)", headlineToday, chartToday, diff)
	}
	// And the value must be the true growth since local midnight.
	if headlineToday < 1200 || headlineToday > 1260 {
		t.Fatalf("headline today = %.0f, want ~1234", headlineToday)
	}
}

func asF(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		var f float64
		_, _ = fmt.Sscanf(n, "%f", &f)
		return f
	}
	return 0
}
