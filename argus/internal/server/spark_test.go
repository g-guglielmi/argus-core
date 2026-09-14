package server

import (
	"testing"
	"time"
)

// dailyDeltas turns a rolling counter total into per-day growth: normal days delta against the
// previous day's close, a fresh item (no previous day) baselines on its own first reading, a
// window-slide dip or stats reset clamps at 0, and an empty day stays 0.
func TestDailyDeltas(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600)
	today0 := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	day := func(off int, hour int) int64 {
		return today0.AddDate(0, 0, off).Add(time.Duration(hour) * time.Hour).Unix()
	}

	t.Run("normal growth with dip and reset", func(t *testing.T) {
		pts := []dailyPt{
			// d-3: closes at 1000
			{day(-3, 1), 900}, {day(-3, 23), 1000},
			// d-2: grows to 2000
			{day(-2, 6), 1500}, {day(-2, 23), 2000},
			// d-1: window-slide dip below the previous close -> clamped to 0
			{day(-1, 12), 1800},
			// today: stats reset to near zero, then grows to 50
			{day(0, 1), 10}, {day(0, 9), 50},
		}
		got := dailyDeltas(pts, today0, 3) // buckets: d-2, d-1, today
		want := []float64{1000, 0, 0}      // reset day: last(50) - prev close(1800) < 0 -> 0
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("bucket %d = %v, want %v (all: %v)", i, got[i], want[i], got)
			}
		}
	})

	t.Run("fresh item baselines on its own first reading", func(t *testing.T) {
		pts := []dailyPt{{day(0, 9), 100}, {day(0, 11), 250}}
		got := dailyDeltas(pts, today0, 7)
		if got[6] != 150 {
			t.Fatalf("today = %v, want 150 (first-reading baseline)", got[6])
		}
		for i := 0; i < 6; i++ {
			if got[i] != 0 {
				t.Fatalf("empty day %d = %v, want 0", i, got[i])
			}
		}
	})

	t.Run("day after a gap uses its own first reading", func(t *testing.T) {
		pts := []dailyPt{
			{day(-3, 23), 1000},
			// d-2, d-1: outage, no readings
			{day(0, 2), 3000}, {day(0, 20), 3400},
		}
		got := dailyDeltas(pts, today0, 3)
		if got[0] != 0 || got[1] != 0 {
			t.Fatalf("outage days = %v/%v, want 0/0", got[0], got[1])
		}
		if got[2] != 400 { // 3400 - 3000, NOT 3400 - 1000 (a 3-day span would lie)
			t.Fatalf("post-gap day = %v, want 400", got[2])
		}
	})

	t.Run("unsorted input is sorted", func(t *testing.T) {
		pts := []dailyPt{{day(0, 9), 500}, {day(-1, 23), 100}, {day(0, 2), 200}, {day(-1, 1), 50}}
		got := dailyDeltas(pts, today0, 2)
		if got[0] != 50 || got[1] != 400 { // d-1: 100-50 (own first); today: 500-100 (prev close)
			t.Fatalf("got %v, want [50 400]", got)
		}
	})

	t.Run("no data", func(t *testing.T) {
		got := dailyDeltas(nil, today0, 7)
		if len(got) != 7 {
			t.Fatalf("len = %d, want 7", len(got))
		}
	})
}

// dailySplitDeltas reconstructs TRUE local-calendar-day counts from a "today so far" counter
// whose source day is UTC-aligned (AdGuard rolls at 02:00 local for a UTC+2 viewer). Readings
// carry the hourly-trend convention: t = the hour's START, v = the counter at the hour's END,
// so a delta between consecutive rows is the growth during the later row's hour.
func TestDailySplitDeltas(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600)
	today0 := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	from := today0.AddDate(0, 0, -7).Unix()
	day := func(off int, hour int) int64 {
		return today0.AddDate(0, 0, off).Add(time.Duration(hour) * time.Hour).Unix()
	}

	pts := []dailyPt{
		// Freshly monitored mid-day: the first reading carries the source day's count so far and
		// must be credited, or day one undercounts. (Out-of-order input on purpose.)
		{day(-1, 22), 800},  // +300 for d-1 (growth during 22:00-23:00 local)
		{day(-1, 9), 500},   // genesis: +500 for d-1
		{day(-1, 23), 1000}, // +200 for d-1 - the hour ENDING at local midnight is yesterday's
		{day(0, 0), 1080},   // +80 for today (growth during 00:00-01:00 local, source day still open)
		{day(0, 1), 1100},   // +20 for today (01:00-02:00 local, just before the source rollover)
		{day(0, 2), 50},     // source rolled at 02:00 local: the new count is growth since the reset
		{day(0, 10), 400},   // live last value: +350 for today
	}
	got := dailySplitDeltas(pts, today0, 7, from)
	if len(got) != 7 {
		t.Fatalf("len = %d, want 7", len(got))
	}
	want := []float64{0, 0, 0, 0, 0, 1000, 500}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bucket %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}

	// A series that starts AT the window edge is an old item, not a fresh one: its first reading's
	// carried value belongs to time before the window and must NOT be credited.
	edge := dailySplitDeltas([]dailyPt{{from, 5000}, {from + 86400 + 3600, 5100}}, today0, 7, from)
	var sum float64
	for _, v := range edge {
		sum += v
	}
	if sum != 100 {
		t.Fatalf("window-edge series credited %v, want only the observed +100", sum)
	}

	if out := dailySplitDeltas(nil, today0, 3, from); len(out) != 3 {
		t.Fatalf("no data: len = %d, want 3", len(out))
	}
}

// rateBuckets derives per-day block rates from the per-day counts; dailyMode routes the shapes.
func TestRateBuckets(t *testing.T) {
	got := rateBuckets([]float64{1000, 500, 0, 200}, []float64{100, 250, 5, 999})
	want := []float64{10, 50, 0, 100} // empty day -> 0; blocked clamped to total -> 100%
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bucket %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	if out := rateBuckets([]float64{100}, nil); out[0] != 0 {
		t.Fatalf("missing blocked series: got %v, want 0", out[0])
	}
	for k, m := range map[string]string{"adguard.queries.today": "max", "adguard.blocked.today": "max", "adguard.block_pct": "rate", "adguard.queries": "delta"} {
		if got := dailyMode(k); got != m {
			t.Fatalf("dailyMode(%s) = %s, want %s", k, got, m)
		}
	}
}
