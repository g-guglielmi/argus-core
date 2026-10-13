// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package report

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// sampleDoc is the report the mockup showed: a month of site1.
func sampleDoc() Doc {
	loc := time.UTC
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 1, 0)
	mbps := func(v float64) string {
		switch {
		case v >= 1e9:
			return trim(v/1e9) + " Gbps"
		case v >= 1e6:
			return trim(math.Round(v/1e5)/10) + " Mbps"
		}
		return trim(v) + " bps"
	}
	traffic := func(base, amp float64, spikes ...int) []Point {
		var pts []Point
		for h := 0; h < 30*24; h++ {
			v := base + amp*(1+math.Sin(float64(h)*2*math.Pi/24))/2
			for _, s := range spikes {
				if h == s {
					v = 890e6
				}
			}
			pts = append(pts, Point{from.Add(time.Duration(h) * time.Hour), v})
		}
		return pts
	}
	var days []Day
	for i := 0; i < 30; i++ {
		days = append(days, Day{Good})
	}
	webDays := append([]Day{}, days...)
	webDays[11], webDays[24] = Day{Warn}, Day{Warn}
	var up []Point
	for h := 0; h < 30*24; h++ {
		up = append(up, Point{from.Add(time.Duration(h) * time.Hour), 1})
	}
	return Doc{
		Title: "Monthly availability, site1", Period: "1 to 30 September 2026",
		Scope: "site1", Made: "Made Oct 1, 2026 at 07:00 (Europe/Rome)", For: "for it@example.com, manager@example.com",
		Tiles: []Tile{
			{"Uptime", "99.99 %", "14 hosts, target 99.9 %", Plain},
			{"Under target", "1 host", "backup1, 99.81 %", Err},
			{"Downtime", "1 h 35 min", "3 hosts; maintenance left out", Plain},
			{"Problems", "12", "3 errors, 9 warnings", Plain},
		},
		Hosts: []HostRow{
			{"gw-site1", "site1/Network", "100 %", "0", "1", "-", Good, false},
			{"web1", "site1/Servers", "99.98 %", "9 min", "4", "6 min", Warn, false},
			{"backup1", "site1/Servers", "99.81 %", "1 h 22 min", "3", "47 min", Err, true},
		},
		HostsMore: "11 more hosts at 100 %, no problems",
		Sensors: []SensorRow{
			{"Port 9 (nas1) traffic", "sw-core", "17.7 Mbps", "266 Mbps", "894 Mbps", "457 Mbps", "1 h 50 min", "0", Warn, Plain},
			{"HTTPS response time", "web1", "97 ms", "185 ms", "1.44 s", "241 ms", "35 min", "9 min", Warn, Err},
		},
		Footnote: "Uptime is the share of checks that found a host up, from its availability sensor. Downtime inside a maintenance window is left out. 95th: the value the sensor stayed under 95 % of the time.",
		Sections: []Section{
			{
				Name: "sw-core", Sub: "site1/Network - UniFi switch - 10.0.0.2", Uptime: "100 %", UptimeTone: Good, Days: days,
				NoProblems: "No problems in the period.",
				Charts: []Chart{{
					Title: "Port 9 (nas1) traffic", Stats: []Stat{{"min", "17.7 Mbps"}, {"avg", "266 Mbps"}, {"max", "894 Mbps"}, {"95th", "457 Mbps"}},
					From: from, To: to, Loc: loc, Label: mbps,
					Series:     []Series{{"In", traffic(20e6, 400e6, 300, 520)}, {"Out", traffic(5e6, 20e6)}},
					Thresholds: []Threshold{{800e6, Warn}},
					Foot:       "in warning 1 h 50 min, in error 0",
				}},
			},
			{
				Name: "web1", Sub: "site1/Servers - Linux (SSH) - 10.0.0.21", Uptime: "99.98 %", UptimeTone: Warn, Days: webDays,
				Problems: []Problem{
					{"Sep 12, 03:14", "6 min", "HTTPS check failed: connection timed out", "HTTPS check", "Alex P.: certificate renewal restarted nginx", Err},
					{"Sep 12, 03:21", "2 h 18 min", "HTTPS response time over 800 ms", "HTTPS response time", "", Warn},
				},
				Charts: []Chart{
					{Title: "SMTP (25)", Kind: UpDownChart, From: from, To: to, Loc: loc, Foot: "no values in the period"},
					{
						Title: "HTTPS check", Kind: UpDownChart, Stats: []Stat{{"uptime", "99.98 %"}, {"down", "9 min in 2 problems"}},
						From: from, To: to, Loc: loc, Series: []Series{{"", up}},
						Down: []Span{{from.Add(266 * time.Hour), from.Add(266*time.Hour + 6*time.Minute)}, {from.Add(590 * time.Hour), from.Add(590*time.Hour + 3*time.Minute)}},
					},
					{
						Title: "HTTPS response time", Stats: []Stat{{"min", "97 ms"}, {"avg", "185 ms"}, {"max", "1.44 s"}, {"95th", "241 ms"}},
						From: from, To: to, Loc: loc, Label: func(v float64) string { return trim(v*1000) + " ms" },
						Series:     []Series{{"", func() []Point { p := traffic(0.1, 0.1); p[266].V = 1.44; p[590].V = 0.9; return p }()}},
						Thresholds: []Threshold{{0.8, Warn}, {1.2, Err}},
						Foot:       "in warning 35 min, in error 9 min",
					},
				},
			},
		},
	}
}

func TestRenderSample(t *testing.T) {
	b, err := Render(sampleDoc())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if out := os.Getenv("REPORT_SAMPLE_OUT"); out != "" {
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n := bytes.Count(b, []byte("/Type /Page\n")) + bytes.Count(b, []byte("/Type /Page ")); n == 0 {
		t.Log("page count not found in the raw PDF (fine: gopdf writes it its own way)")
	}
}

// A long table carries over to the next page instead of running off the bottom.
func TestRenderLongTable(t *testing.T) {
	d := sampleDoc()
	d.Hosts = nil
	for i := 0; i < 120; i++ {
		d.Hosts = append(d.Hosts, HostRow{Name: fmt.Sprintf("host%03d", i), Group: "site1/Servers", Uptime: "100 %", Downtime: "0", Problems: "0", Longest: "-", UptimeTone: Good})
	}
	if _, err := Render(d); err != nil {
		t.Fatal(err)
	}
}

// A percentage that reaches 100 stops at 100 %: the room above it doesn't make the axis 120 or 200.
func TestScaleCeil(t *testing.T) {
	now := time.Now()
	c := Chart{Ceil: 100, Series: []Series{{Points: []Point{{now, 0}, {now.Add(time.Hour), 100}}}}}
	if lo, hi, _ := scale(c); lo != 0 || hi != 100 {
		t.Fatalf("loss up to 100 %%: %v to %v", lo, hi)
	}
	c.Series[0].Points[1].V = 37
	if _, hi, _ := scale(c); hi != 40 {
		t.Fatalf("loss up to 37 %%: to %v, want 40", hi)
	}
	c.Ceil = 0
	c.Series[0].Points[1].V = 100
	if _, hi, _ := scale(c); hi != 120 {
		t.Fatalf("no ceiling: to %v, want 120", hi)
	}
}

func TestNiceCeil(t *testing.T) {
	for _, c := range []struct {
		v    float64
		b    bool
		want float64
	}{{894e6, false, 1e9}, {0.9, false, 1}, {1.44, false, 1.5}, {72.2, false, 80}, {48, false, 50}, {23, false, 25}, {108, false, 120},
		{600 * 1024 * 1024, true, 600 * 1024 * 1024}, {1000 * 1024 * 1024, true, 1024 * 1024 * 1024}, {3, false, 3}} {
		if got := niceCeil(c.v, c.b); math.Abs(got-c.want) > 1e-9*c.want {
			t.Errorf("niceCeil(%v, %v) = %v, want %v", c.v, c.b, got, c.want)
		}
	}
}
