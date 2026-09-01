package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"
	"time"

	"argus/internal/zabbix"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// demoSeries is a synthetic 2-hour trend used to preview the graph from the Test button.
func demoSeries() []float64 {
	out := make([]float64, 48)
	for i := range out {
		out[i] = 40 + 30*math.Sin(float64(i)/6) + float64(i)/2.5
	}
	return out
}

// alertSeries fetches ~2 hours of history for one numeric item (value type 0/3), oldest→newest,
// downsampled for a compact chart. Returns nil on error.
func alertSeries(ctx context.Context, zbx *zabbix.Client, itemID, valueType string) []float64 {
	from := time.Now().Add(-2 * time.Hour).Unix()
	pts, err := zbx.HistoryMulti(ctx, []string{itemID}, atoi(valueType), from)
	if err != nil {
		return nil
	}
	var vals []float64
	for _, p := range pts {
		if v := pf(p.Value); v != nil {
			vals = append(vals, *v)
		}
	}
	return downsample(vals, 160)
}

// alertChart renders a sensor's 2-hour trend PNG in the given state's color, or nil when there's
// no usable history (non-numeric item, no data, or a fetch error) - the alert then omits the graph.
// It reads the item's units so the Y axis scales them like the app (bytes, bits, uptime).
func alertChart(ctx context.Context, zbx *zabbix.Client, itemID, state string) []byte {
	if itemID == "" {
		return nil
	}
	items, err := zbx.ItemsByIDs(ctx, []string{itemID})
	if err != nil {
		return nil
	}
	it, ok := items[itemID]
	if !ok || (it.ValueType != "0" && it.ValueType != "3") {
		return nil // non-numeric item: no chart
	}
	series := alertSeries(ctx, zbx, itemID, it.ValueType)
	if len(series) < 2 {
		return nil
	}
	r, g, b := statusRGB(state)
	return renderChart(series, r, g, b, it.Units)
}

// renderChart draws a compact 2-hour trend as a PNG (white background, filled line in the status
// color) with labeled axes: min/mid/max on the Y axis (scaled by the item's units, like the app)
// and relative time (2h ago → now) on the X axis. Returns nil for <2 points.
func renderChart(vals []float64, cr, cg, cb uint8, units string) []byte {
	const w, h = 600, 200
	// Room on the left for Y labels (wider, to fit unit suffixes like "5.2GBps") and along the bottom.
	const mL, mR, mT, mB = 60, 12, 12, 26
	pw, ph := w-mL-mR, h-mT-mB

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if len(vals) < 2 {
		return encodePNG(img)
	}

	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if max == min {
		max = min + 1 // avoid a flat divide-by-zero; centers the line
	}
	n := len(vals)
	xAt := func(i int) int { return mL + i*pw/(n-1) }
	yAt := func(v float64) int { return mT + int((1-(v-min)/(max-min))*float64(ph)) }

	line := color.RGBA{cr, cg, cb, 255}
	fill := blendWhite(cr, cg, cb, 0.16)
	grid := color.RGBA{236, 236, 236, 255}
	axis := color.RGBA{206, 206, 206, 255}
	label := color.RGBA{120, 120, 120, 255}
	baseY := h - mB

	// Horizontal gridlines at max / mid / min, each with its value label to the left.
	for _, gl := range []struct {
		v float64
		y int
	}{{max, mT}, {(min + max) / 2, mT + ph/2}, {min, baseY}} {
		for x := mL; x < w-mR; x++ {
			img.Set(x, gl.y, grid)
		}
		s := axisLabel(gl.v, units)
		tw := textWidth(s)
		drawText(img, mL-6-tw, gl.y+4, s, label)
	}

	for i := 0; i < n-1; i++ {
		x0, y0 := xAt(i), yAt(vals[i])
		x1, y1 := xAt(i+1), yAt(vals[i+1])
		if x1 <= x0 {
			x1 = x0 + 1
		}
		for x := x0; x <= x1; x++ {
			t := float64(x-x0) / float64(x1-x0)
			y := y0 + int(t*float64(y1-y0))
			for yy := y; yy < baseY; yy++ {
				img.Set(x, yy, fill)
			}
			for d := -1; d <= 1; d++ { // ~2px line
				img.Set(x, y+d, line)
			}
		}
	}

	// last-point marker
	lx, ly := xAt(n-1), yAt(vals[n-1])
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			if dx*dx+dy*dy <= 9 {
				img.Set(lx+dx, ly+dy, line)
			}
		}
	}
	// baseline (x axis)
	for x := mL; x < w-mR; x++ {
		img.Set(x, baseY, axis)
	}
	// X labels: the series spans ~2 hours ending now, so label thirds by elapsed time.
	xTicks := []struct {
		frac float64
		s    string
	}{{0, "2h ago"}, {0.5, "1h ago"}, {1, "now"}}
	for _, xt := range xTicks {
		cx := mL + int(xt.frac*float64(pw))
		tw := textWidth(xt.s)
		tx := cx - tw/2
		if tx < mL {
			tx = mL
		} else if tx+tw > w-mR {
			tx = w - mR - tw
		}
		drawText(img, tx, baseY+16, xt.s, label)
	}
	return encodePNG(img)
}

// trimFloat formats v with up to dec decimals, trailing zeros (and a bare dot) trimmed.
func trimFloat(v float64, dec int) string {
	s := strconv.FormatFloat(v, 'f', dec, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// axisNum formats a unitless value for an axis tick in compact form - never scientific notation.
// Magnitudes >= 1000 get SI suffixes (706k, 70.6M, 1.2G); smaller values keep ~3 significant figures.
func axisNum(v float64) string {
	if v == 0 {
		return "0"
	}
	a := math.Abs(v)
	sign := ""
	if v < 0 {
		sign = "-"
	}
	suffix := ""
	for _, s := range []string{"k", "M", "G", "T", "P"} {
		if a < 1000 {
			break
		}
		a /= 1000
		suffix = s
	}
	dec := 0 // decimals scaled to hold ~3 significant figures
	switch {
	case a < 1:
		dec = 3
	case a < 10:
		dec = 2
	case a < 100:
		dec = 1
	}
	return sign + trimFloat(a, dec) + suffix
}

// Unit families mirror the app (web/src/App.tsx): bytes scale by 1024, bits by 1000.
var byteAxisUnits = []string{"B", "KB", "MB", "GB", "TB", "PB"}
var bitAxisUnits = []string{"bps", "Kbps", "Mbps", "Gbps", "Tbps"}

// scaleUnit reduces n by base until it fits a unit; returns "<value><unit>" (compact, no space),
// mirroring the app's scaleBy: integer at the base unit, else up to 2 decimals trimmed.
func scaleUnit(n, base float64, units []string) (string, string) {
	v, i := n, 0
	for math.Abs(v) >= base && i < len(units)-1 {
		v /= base
		i++
	}
	if i == 0 {
		return trimFloat(math.Round(v), 0), units[i]
	}
	return trimFloat(v, 2), units[i]
}

// axisDuration renders seconds as a single decimal unit for the axis (817d, 8.2d, 4.1h, 45m, 11s).
func axisDuration(sec float64) string {
	a := math.Abs(sec)
	switch {
	case a >= 86400:
		return trimFloat(sec/86400, 1) + "d"
	case a >= 3600:
		return trimFloat(sec/3600, 1) + "h"
	case a >= 60:
		return trimFloat(sec/60, 1) + "m"
	default:
		return trimFloat(sec, 0) + "s"
	}
}

// axisLabel formats an axis tick using the app's unit scaling: bytes (1024), bits (1000), and uptime
// as a duration; other units fall back to a compact SI magnitude with the raw unit appended.
func axisLabel(v float64, units string) string {
	switch units {
	case "uptime":
		return axisDuration(v)
	case "B":
		val, u := scaleUnit(v, 1024, byteAxisUnits)
		return val + u
	case "Bps":
		val, u := scaleUnit(v, 1024, byteAxisUnits)
		return val + u + "ps"
	case "bps":
		val, u := scaleUnit(v, 1000, bitAxisUnits)
		return val + u
	case "":
		return axisNum(v)
	default:
		return axisNum(v) + units
	}
}

// textWidth returns the pixel width of s in the basicfont face used for labels.
func textWidth(s string) int {
	return font.MeasureString(basicfont.Face7x13, s).Round()
}

// drawText draws s with its left edge at x and text baseline at y, in the given color.
func drawText(img *image.RGBA, x, y int, s string, col color.Color) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

// blendWhite mixes a color with white at the given alpha (0..1) → an opaque light tint.
func blendWhite(r, g, b uint8, a float64) color.RGBA {
	mix := func(c uint8) uint8 { return uint8(float64(c)*a + 255*(1-a)) }
	return color.RGBA{mix(r), mix(g), mix(b), 255}
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// statusRGB returns the chart line color for a state, matching the UI palette.
func statusRGB(state string) (uint8, uint8, uint8) {
	switch state {
	case "error":
		return 0xE2, 0x56, 0x4D
	case "warning":
		return 0xE0, 0xA5, 0x3A
	default:
		return 0x3F, 0xA6, 0x6A
	}
}
