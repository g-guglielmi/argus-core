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

// dailyMaxes handles the ".today" sawtooth counters (reset at midnight, rise all day): a closed
// day's bucket is its final total (the peak), today's is the running total, an empty day stays 0.
func TestDailyMaxes(t *testing.T) {
	loc := time.FixedZone("viewer", 2*3600)
	today0 := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	day := func(off int, hour int) int64 {
		return today0.AddDate(0, 0, off).Add(time.Duration(hour) * time.Hour).Unix()
	}

	pts := []dailyPt{
		// d-2: rises to 1000 by the day's end
		{day(-2, 6), 400}, {day(-2, 23), 1000},
		// d-1: rises to 1012 (out-of-order input on purpose)
		{day(-1, 23), 1012}, {day(-1, 3), 100},
		// today: post-midnight reset, running total 1234 (the live lastvalue)
		{day(0, 0), 3}, {day(0, 10), 1234},
	}
	got := dailyMaxes(pts, today0, 7)
	if len(got) != 7 {
		t.Fatalf("len = %d, want 7", len(got))
	}
	want := []float64{0, 0, 0, 0, 1000, 1012, 1234}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bucket %d = %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	if out := dailyMaxes(nil, today0, 3); len(out) != 3 {
		t.Fatalf("no data: len = %d, want 3", len(out))
	}
}
