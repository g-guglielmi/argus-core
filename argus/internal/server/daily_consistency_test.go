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

// The row headline ("N queries today") and the bar chart's today bar are two views of the same
// day bucket and MUST agree. The source items are "today so far" sawtooth counters (AdGuard's own
// per-day stats: reset at midnight, rise through the day); a day's total is its peak reading.
// This drives both REAL data paths - /api/daily's bucket math and the chart's trends+raw-tail
// max-of-day - against a mock Zabbix serving one synthetic sawtooth and compares them end to end
// (lab bug 2026-09-14: the previous rolling-total delta approach made the views disagree and read
// "0 blocked" all day when the retention window shed faster than blocks arrived).
func TestDailyVsChartConsistency(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600) // the lab viewer: UTC+2, off=-120
	now := time.Date(2026, 9, 14, 14, 37, 22, 0, loc)
	today0 := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	// Sawtooth: per-day totals; today reaches 1234 at "now". Within a day the count rises
	// linearly; the day boundary is the SOURCE's - AdGuard's stats days are UTC-aligned, so for
	// this UTC+2 viewer the counter rolls at 02:00 local (the lab bug: at 00:06 local the raw
	// reading still carried yesterday's total).
	nowUTCDay := now.Unix() / 86400
	dataStart := time.Unix((nowUTCDay-2)*86400, 0)
	dayTotal := map[int64]float64{
		nowUTCDay - 2: 1000,
		nowUTCDay - 1: 1012,
		nowUTCDay:     1234 * 86400 / float64(now.Unix()%86400),
	}
	valueAt := func(ts time.Time) float64 {
		if ts.Before(dataStart) {
			return 0
		}
		return dayTotal[ts.Unix()/86400] * float64(ts.Unix()%86400) / 86400
	}
	lastValue := valueAt(now)

	// Mock Zabbix JSON-RPC: item.get (live lastvalue), trend.get (UTC-hour rows, CLOSED hours only
	// - trends lag the open hour; max = the value at the hour's end), history.get (raw 1-min polls).
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
				"itemid": "1", "hostid": "10", "name": "DNS queries today", "key_": "adguard.queries.today",
				"units": "", "value_type": "3",
				"lastvalue": fmt.Sprintf("%.0f", lastValue), "lastclock": fmt.Sprintf("%d", now.Unix()),
			}}
		case "trend.get":
			from := int64(asF(req.Params["time_from"]))
			till := int64(asF(req.Params["time_till"]))
			rows := []map[string]string{}
			lastClosed := now.Truncate(time.Hour)
			for h := time.Unix(from, 0).Truncate(time.Hour); h.Before(lastClosed); h = h.Add(time.Hour) {
				if h.Unix() < from || h.Unix() > till || h.Add(time.Hour).Before(dataStart) {
					continue
				}
				rows = append(rows, map[string]string{
					"clock":     fmt.Sprintf("%d", h.Unix()),
					"value_min": fmt.Sprintf("%.0f", valueAt(h)),
					"value_avg": fmt.Sprintf("%.0f", valueAt(h.Add(30*time.Minute))),
					"value_max": fmt.Sprintf("%.0f", valueAt(h.Add(time.Hour-time.Second))),
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
				if ts.After(now) {
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

	// --- The row headline: /api/daily's sawtooth path (trend max + live lastvalue -> dailyMaxes).
	// handleDaily uses time.Now(), so assemble the same inputs anchored on the synthetic "now".
	tps, err := s.zbx.Trends(t.Context(), "1", today0.AddDate(0, 0, -7).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	pts := make([]dailyPt, 0, len(tps)+1)
	for _, p := range tps {
		if v := pf(p.ValueMax); v != nil {
			pts = append(pts, dailyPt{t: atoi64(p.Clock), v: *v})
		}
	}
	pts = append(pts, dailyPt{t: now.Unix(), v: lastValue})
	daily := dailyMaxes(pts, today0, 7)
	headlineToday := daily[len(daily)-1]

	// --- The chart's today bar: trends (7d window) + raw 2h tail, merged, then buildBarPlot's
	// per-local-day PEAK bucketing.
	chartTrends, err := s.zbx.Trends(t.Context(), "1", now.Add(-7*24*time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("chart trends: %v", err)
	}
	rawTail, err := s.zbx.History(t.Context(), "1", 3, now.Add(-2*time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("raw tail: %v", err)
	}
	byDay := map[int64]float64{}
	dayStart := func(ts int64) int64 {
		y, m, d := time.Unix(ts, 0).In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc).Unix()
	}
	add := func(ts int64, v float64) {
		// like buildBarPlot: group by the source's UTC day, credit its midpoint's local date
		d := dayStart(ts/86400*86400 + 43200)
		if v > byDay[d] {
			byDay[d] = v
		}
	}
	for _, p := range chartTrends {
		// hour MAX, like buildBarPlot's `p.hi ?? p.v`.
		if v := pf(p.ValueMax); v != nil {
			add(atoi64(p.Clock), *v)
		}
	}
	for _, p := range rawTail {
		if v := pf(p.Value); v != nil {
			add(atoi64(p.Clock), *v)
		}
	}
	chartToday := byDay[today0.Unix()]
	chartYesterday := byDay[today0.AddDate(0, 0, -1).Unix()]

	// Today: both views live on the same running total.
	if diff := headlineToday - chartToday; diff > 2 || diff < -2 {
		t.Fatalf("today mismatch: headline (daily) = %.0f, chart bar = %.0f", headlineToday, chartToday)
	}
	if headlineToday < 1232 || headlineToday > 1236 {
		t.Fatalf("headline today = %.0f, want ~1234 (the true count since local midnight)", headlineToday)
	}
	// Yesterday: the closed day's bar is its final total, both views.
	if daily[len(daily)-2] != chartYesterday {
		t.Fatalf("yesterday mismatch: daily = %.0f, chart = %.0f", daily[len(daily)-2], chartYesterday)
	}
	if chartYesterday < 1010 || chartYesterday > 1013 {
		t.Fatalf("yesterday = %.0f, want ~1012 (the day's final total)", chartYesterday)
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
