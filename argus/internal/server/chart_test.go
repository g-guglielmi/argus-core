// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestAxisNum(t *testing.T) {
	cases := map[float64]string{
		0: "0", 11: "11", 3.14159: "3.14", 706000: "706k",
		70600000: "70.6M", 35300000: "35.3M", 1200000000: "1.2G", 0.5: "0.5", -4200: "-4.2k",
	}
	for in, want := range cases {
		if got := axisNum(in); got != want {
			t.Errorf("axisNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAxisLabel(t *testing.T) {
	cases := []struct {
		v     float64
		units string
		want  string
	}{
		{70600000, "uptime", "817.1d"}, // uptime seconds → days
		{708480, "uptime", "8.2d"},
		{4680, "uptime", "1.3h"},
		{45, "uptime", "45s"},
		{5368709120, "B", "5GB"},     // bytes, 1024-based
		{1610612736, "B", "1.5GB"},   // 1.5 GiB
		{512, "B", "512B"},           // base unit → integer, no scaling
		{45000000, "bps", "45Mbps"},  // bits, 1000-based
		{5000000, "Bps", "4.77MBps"}, // bytes/s → MBps
		{45.2, "%", "45.2%"},         // arbitrary unit appended
		{70600000, "", "70.6M"},      // unitless → SI compact
	}
	for _, c := range cases {
		if got := axisLabel(c.v, c.units); got != c.want {
			t.Errorf("axisLabel(%v, %q) = %q, want %q", c.v, c.units, got, c.want)
		}
	}
}

// TestRenderChartBands checks the alert graph is coloured by value: a series crossing both
// thresholds paints normal, warning AND error pixels, while an unbanded chart uses only the status
// colour. ARGUS_CHART_OUT=<dir> also writes the PNGs for a visual check.
func TestRenderChartBands(t *testing.T) {
	vals := demoSeries()
	banded := renderChart(vals, 0xE2, 0x56, 0x4D, "%", demoThresholds())
	plain := renderChart(vals, 0xE2, 0x56, 0x4D, "%", nil)
	if dir := os.Getenv("ARGUS_CHART_OUT"); dir != "" {
		_ = os.WriteFile(dir+"/banded.png", banded, 0o644)
		_ = os.WriteFile(dir+"/plain.png", plain, 0o644)
	}
	count := func(b []byte) map[[3]uint8]int {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[[3]uint8]int{}
		bd := img.Bounds()
		for y := bd.Min.Y; y < bd.Max.Y; y++ {
			for x := bd.Min.X; x < bd.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				seen[[3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}]++
			}
		}
		return seen
	}
	rgb := func(c interface{ RGBA() (r, g, b, a uint32) }) [3]uint8 {
		r, g, b, _ := c.RGBA()
		return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
	}
	bc := count(banded)
	for name, c := range map[string][3]uint8{"normal": rgb(bandNormal), "warning": rgb(bandWarn), "error": rgb(bandErr)} {
		if bc[c] == 0 {
			t.Errorf("banded chart has no %s-coloured line pixels", name)
		}
	}
	pc := count(plain)
	if pc[rgb(bandNormal)] != 0 || pc[rgb(bandWarn)] != 0 {
		t.Errorf("unbanded chart should draw only in the status colour")
	}
	if pc[rgb(bandErr)] == 0 {
		t.Errorf("unbanded chart lost its status-coloured line")
	}
}

// TestRenderStateChart checks an up/down sensor's graph shades the time it was down, not the time it
// was up: a column in a down stretch is red from top to bottom, one in an up stretch stays white off
// the line, and the line runs on the "up" level while up.
func TestRenderStateChart(t *testing.T) {
	vals := make([]float64, 120)
	for i := range vals {
		vals[i] = 1
		if i >= 40 && i < 80 {
			vals[i] = 0 // down in the middle third
		}
	}
	b := renderStateChart(vals)
	if dir := os.Getenv("ARGUS_CHART_OUT"); dir != "" {
		_ = os.WriteFile(dir+"/state.png", b, 0o644)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	rgb := func(x, y int) [3]uint8 {
		r, g, bl, _ := img.At(x, y).RGBA()
		return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
	}
	shade := blendWhite(bandErr.R, bandErr.G, bandErr.B, 0.18)
	want := [3]uint8{shade.R, shade.G, shade.B}
	const mL, pw, mid = 60, 528, 90 // the plot's left edge and width; a height between the two levels
	downX := mL + 60*pw/120         // the middle of the down stretch
	upX := mL + 20*pw/120           // the middle of the first up stretch
	for _, y := range []int{18, mid, 150} {
		if got := rgb(downX, y); got != want {
			t.Fatalf("down stretch at y=%d is %v, want the red shade %v", y, got, want)
		}
	}
	if got := rgb(upX, mid); got != [3]uint8{255, 255, 255} {
		t.Fatalf("up stretch should stay white off the line, got %v", got)
	}
	if got := rgb(upX, 12+14); got != [3]uint8{bandNormal.R, bandNormal.G, bandNormal.B} {
		t.Fatalf("the line should run on the up level while up, got %v", got)
	}
	if got := rgb(downX, 174-14); got != [3]uint8{bandErr.R, bandErr.G, bandErr.B} {
		t.Fatalf("the line should run on the down level while down, got %v", got)
	}
	// Down right now: the last reading is shaded too, up to "now".
	vals[len(vals)-1] = 0
	img, err = png.Decode(bytes.NewReader(renderStateChart(vals)))
	if err != nil {
		t.Fatal(err)
	}
	if got := rgb(mL+pw-2, mid); got != want {
		t.Fatalf("the latest reading, down, should be shaded, got %v", got)
	}
}
