// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import "testing"

// A run is its speedtest.ok reading with the readings stored at the same moment, newest first; a
// failed run keeps its reason, and the test site is the one in effect at the time.
func TestJoinRuns(t *testing.T) {
	vals := map[string]map[int64]string{
		"speedtest.ok":      {1000: "1", 22600: "0", 44200: "1"},
		"speedtest.error":   {1000: "", 22600: "the download: Cloudflare refused it (HTTP 429)", 44200: ""},
		"speedtest.down":    {1000: "2105.27", 44201: "2347.12"}, // a second off still joins
		"speedtest.up":      {1000: "725.16", 44200: "706.48"},
		"speedtest.latency": {1000: "0.019", 22600: "0.021", 44200: "0.018"},
		"speedtest.site":    {1000: "MXP", 30000: "FRA"},
	}
	runs := joinRuns(vals)
	if len(runs) != 3 || runs[0].T != 44200 || runs[2].T != 1000 {
		t.Fatalf("runs newest first: %+v", runs)
	}
	if !runs[0].OK || runs[0].Down == nil || *runs[0].Down != 2347.12 || runs[0].Site != "FRA" {
		t.Fatalf("newest run: %+v", runs[0])
	}
	if f := runs[1]; f.OK || f.Error != "the download: Cloudflare refused it (HTTP 429)" || f.Down != nil || f.Site != "MXP" || f.Latency == nil {
		t.Fatalf("failed run: %+v", f)
	}
	if runs[2].Error != "" || runs[2].Up == nil || *runs[2].Up != 725.16 {
		t.Fatalf("oldest run: %+v", runs[2])
	}
	if len(joinRuns(map[string]map[int64]string{})) != 0 {
		t.Fatal("no runs from no readings")
	}
}

// The speed test's sensors read once per run, so their sparkline covers a week; others keep theirs.
func TestMeasuredByRuns(t *testing.T) {
	for _, k := range []string{"speedtest.down", "speedtest.loaded.up", "speedtest.ok"} {
		if !measuredByRuns(k) {
			t.Errorf("%s: want measured by runs", k)
		}
	}
	for _, k := range []string{"icmppingsec", "unifi.speedtest.down", "net.if.in[eth0]"} {
		if measuredByRuns(k) {
			t.Errorf("%s: not measured by runs", k)
		}
	}
}

// Thinning a series keeps its peaks and dips, in order, within the limit.
func TestDownsampleKeepsExtremes(t *testing.T) {
	vals := make([]float64, 60)
	for i := range vals {
		vals[i] = 300
	}
	vals[17], vals[54], vals[40] = 2700, 1200, 40 // two spikes and a dip, between any every-k-th pick
	out := downsample(vals, 24)
	if len(out) > 24 {
		t.Fatalf("len %d > 24", len(out))
	}
	has := func(v float64) int {
		for i, x := range out {
			if x == v {
				return i
			}
		}
		return -1
	}
	a, d, b := has(2700), has(40), has(1200)
	if a < 0 || b < 0 || d < 0 || !(a < d && d < b) {
		t.Fatalf("extremes lost or out of order: %v", out)
	}
	if got := downsample([]float64{1, 2, 3}, 24); len(got) != 3 {
		t.Fatalf("a short series stays whole: %v", got)
	}
}
