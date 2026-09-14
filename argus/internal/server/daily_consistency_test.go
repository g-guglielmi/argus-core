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

// /api/daily is the single source for AdGuard's daily buckets (row, mini bars, big chart). Its
// counters are "today so far" sawtooths whose SOURCE day is UTC-aligned (AdGuardHome#4560), so
// for a UTC+2 viewer they roll at 02:00 local - and the buckets must still be TRUE local
// calendar days, reconstructed from within-source-day growth deltas. This drives the real data
// path (mock Zabbix -> Trends/item.get -> dailySplitDeltas) through a synthetic sawtooth and
// checks two moments against ground truth: mid-afternoon, and 00:06 local - the lab bug where
// the pre-rollover reading used to duplicate yesterday's total into today.
func TestDailyLocalDayReconstruction(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600) // the lab viewer: UTC+2, off=-120
	// Sawtooth: resets at UTC midnight (02:00 local); rises linearly within each UTC day.
	base := time.Date(2026, 9, 14, 14, 37, 22, 0, loc)
	baseUTCDay := base.Unix() / 86400
	rate := map[int64]float64{ // growth per FULL utc day
		baseUTCDay - 3: 900,
		baseUTCDay - 2: 1000,
		baseUTCDay - 1: 1012,
		baseUTCDay:     2400, // 100/h - today's running count
	}
	dataStart := time.Unix((baseUTCDay-3)*86400, 0)
	valueAt := func(ts time.Time) float64 {
		if ts.Before(dataStart) {
			return 0
		}
		return rate[ts.Unix()/86400] * float64(ts.Unix()%86400) / 86400
	}
	// Ground truth for a LOCAL day [00:00, 24:00): the tail of one UTC day plus the head of the
	// next: growth = v(rollover^-) - v(local midnight) + v(local midnight next, or now).
	localDayTruth := func(d0 time.Time, until time.Time) float64 {
		end := d0.AddDate(0, 0, 1)
		if until.Before(end) {
			end = until
		}
		roll := time.Unix((d0.Unix()/86400+1)*86400, 0) // the UTC midnight inside the local day
		if end.Before(roll) || end.Equal(roll) {
			return valueAt(end) - valueAt(d0)
		}
		full := rate[d0.Unix()/86400] // the closing value of the UTC day that ends mid local-day
		return (full - valueAt(d0)) + valueAt(end)
	}

	for _, tc := range []struct {
		name string
		now  time.Time
	}{
		{"mid-afternoon", base},
		{"just after local midnight (pre-rollover, the lab bug)", time.Date(2026, 9, 15, 0, 6, 0, 0, loc)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := tc.now
			y, mo, d := now.In(loc).Date()
			today0 := time.Date(y, mo, d, 0, 0, 0, 0, loc)
			lastValue := valueAt(now)

			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
					ID     any            `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var result any
				switch req.Method {
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
							"value_min": fmt.Sprintf("%.3f", valueAt(h)),
							"value_avg": fmt.Sprintf("%.3f", valueAt(h.Add(30*time.Minute))),
							"value_max": fmt.Sprintf("%.3f", valueAt(h.Add(time.Hour-time.Second))),
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
			from := today0.AddDate(0, 0, -7).Unix()
			tps, err := s.zbx.Trends(t.Context(), "1", from, now.Unix())
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
			got := dailySplitDeltas(pts, today0, 7, from)

			wantToday := localDayTruth(today0, now)
			wantYesterday := localDayTruth(today0.AddDate(0, 0, -1), now)
			// Trend rows are hourly and the max is sampled 1s before the hour ends, so allow a
			// few counts of rounding - never a day's worth.
			near := func(got, want float64) bool { d := got - want; return d < 4 && d > -4 }
			if !near(got[6], wantToday) {
				t.Fatalf("today = %.1f, want %.1f (yesterday's total is %.0f - matching it means the old duplicate-bar bug)", got[6], wantToday, wantYesterday)
			}
			if !near(got[5], wantYesterday) {
				t.Fatalf("yesterday = %.1f, want %.1f", got[5], wantYesterday)
			}
		})
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
