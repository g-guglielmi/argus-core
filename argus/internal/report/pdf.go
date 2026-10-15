// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package report

import (
	_ "embed"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signintech/gopdf"
)

// Open Sans, Apache License 2.0 (fonts/README.md): a plain sans with an ordinary hyphen and zero.
var (
	//go:embed fonts/OpenSans-Regular.ttf
	fontRegular []byte
	//go:embed fonts/OpenSans-Bold.ttf
	fontBold []byte
)

// A4 in points, and the frame every page is laid out in.
const (
	pageW    = 595.28
	pageH    = 841.89
	marginL  = 42.0
	marginR  = 42.0
	top      = 46.0
	bottom   = 796.0 // content stops here; the footer sits below
	contentW = pageW - marginL - marginR
)

type rgb struct{ r, g, b uint8 }

// The palette: the app's tokens, on paper.
var (
	cInk        = rgb{28, 35, 48}
	cMuted      = rgb{125, 135, 148}
	cFaint      = rgb{229, 233, 238}
	cRule       = rgb{196, 203, 212}
	cAccent     = rgb{0x1F, 0x9C, 0xBF}
	cGood       = rgb{47, 158, 85}
	cWarnText   = rgb{196, 138, 18}
	cErrText    = rgb{214, 69, 60}
	cWarn       = rgb{0xE0, 0xA5, 0x3A}
	cErr        = rgb{0xE2, 0x56, 0x4D}
	cTint       = rgb{253, 238, 238}
	cTileBorder = rgb{221, 226, 232}
	cStripGood  = rgb{63, 170, 99}
	cWhite      = rgb{255, 255, 255}
	cWarnInk    = rgb{0x1B, 0x14, 0x05}
	// A chart's channels, in the app's order (SERIES_COLORS).
	seriesColors = []rgb{{0x2e, 0xa8, 0xc9}, {0xe0, 0xb5, 0x3a}, {0x3a, 0xa8, 0x56}, {0xe0, 0x80, 0x3a}, {0xc9, 0x56, 0x4f}, {0x9b, 0x6f, 0xd6}}
	cFill        = rgb{226, 243, 248} // the first channel's colour at 12 % on white
	cDownShade   = rgb{250, 222, 220} // the error colour at 20 %: the time a group's host was down
)

func toneInk(t Tone) rgb {
	switch t {
	case Good:
		return cGood
	case Warn:
		return cWarnText
	case Err:
		return cErrText
	case Muted:
		return cMuted
	}
	return cInk
}

type writer struct {
	p   *gopdf.GoPdf
	y   float64
	err error
	sec string // the host whose section is being drawn: a page it runs onto says whose it is
}

// Render lays the report out on A4 pages and returns the PDF.
func Render(d Doc) ([]byte, error) {
	p := &gopdf.GoPdf{}
	p.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4, Unit: gopdf.UnitPT})
	p.SetInfo(gopdf.PdfInfo{Title: d.Title + ", " + d.Period, Creator: "Argus", Producer: "Argus", CreationDate: time.Now()})
	if err := p.AddTTFFontData("r", fontRegular); err != nil {
		return nil, err
	}
	if err := p.AddTTFFontData("b", fontBold); err != nil {
		return nil, err
	}
	w := &writer{p: p}
	w.page()
	w.cover(d)
	w.summary(d)
	for _, s := range d.Sections {
		w.section(s)
	}
	n := p.GetNumberOfPages()
	for i := 1; i <= n; i++ {
		if err := p.SetPage(i); err != nil {
			return nil, err
		}
		w.footer(d, i, n)
	}
	if w.err != nil {
		return nil, w.err
	}
	return p.GetBytesPdfReturnErr()
}

// --- drawing helpers ---

func (w *writer) page() {
	w.p.AddPage()
	w.y = top
	if w.sec != "" {
		w.font(true, 8.5, cMuted)
		w.text(marginL, w.y+8, w.fit(w.sec+", continued", contentW))
		w.y += 14
	}
}

// need starts a new page unless h more points fit on this one.
func (w *writer) need(h float64) {
	if w.y+h > bottom {
		w.page()
	}
}

func (w *writer) font(bold bool, size float64, c rgb) {
	fam := "r"
	if bold {
		fam = "b"
	}
	if err := w.p.SetFont(fam, "", size); err != nil && w.err == nil {
		w.err = err
	}
	w.p.SetCharSpacing(0)
	w.p.SetTextColor(c.r, c.g, c.b)
}

// caps sets the small spaced capitals of labels and headings.
func (w *writer) caps(bold bool, size float64, c rgb) {
	w.font(bold, size, c)
	w.p.SetCharSpacing(size * 0.08)
}

func (w *writer) width(s string) float64 {
	v, err := w.p.MeasureTextWidth(s)
	if err != nil {
		return 0
	}
	return v
}

// text draws s with its baseline at y.
func (w *writer) text(x, y float64, s string) {
	if s == "" {
		return
	}
	w.p.SetXY(x, y)
	if err := w.p.Text(s); err != nil && w.err == nil {
		w.err = err
	}
}

func (w *writer) right(x, y float64, s string) { w.text(x-w.width(s), y, s) }

// fit shortens s with an ellipsis until it is at most max wide.
func (w *writer) fit(s string, max float64) string {
	if w.width(s) <= max {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && w.width(string(r)+"…") > max {
		r = r[:len(r)-1]
	}
	return strings.TrimRight(string(r), " ") + "…"
}

// wrap breaks s into lines at most max wide, at spaces (a word too long for a line is shortened).
func (w *writer) wrap(s string, max float64) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line == "" {
				line = word
				continue
			}
			if w.width(line+" "+word) <= max {
				line += " " + word
				continue
			}
			lines = append(lines, w.fit(line, max))
			line = word
		}
		if line != "" {
			lines = append(lines, w.fit(line, max))
		}
	}
	return lines
}

func (w *writer) fill(c rgb)   { w.p.SetFillColor(c.r, c.g, c.b) }
func (w *writer) stroke(c rgb) { w.p.SetStrokeColor(c.r, c.g, c.b) }

func (w *writer) rect(x, y, wd, ht float64, c rgb) {
	if wd <= 0 || ht <= 0 {
		return
	}
	w.fill(c)
	w.p.RectFromUpperLeftWithStyle(x, y, wd, ht, "F")
}

func (w *writer) round(x, y, wd, ht, r float64, style string) {
	if err := w.p.Rectangle(x, y, x+wd, y+ht, style, r, 6); err != nil && w.err == nil {
		w.err = err
	}
}

func (w *writer) line(x1, y1, x2, y2 float64, c rgb, lw float64) {
	w.stroke(c)
	w.p.SetLineWidth(lw)
	w.p.Line(x1, y1, x2, y2)
}

func (w *writer) dashed(on bool) {
	if on {
		w.p.SetCustomLineType([]float64{3, 2.2}, 0)
	} else {
		w.p.SetLineType("solid")
	}
}

// --- page 1 ---

func (w *writer) cover(d Doc) {
	y := w.y
	logoBottom := y
	if d.Logo != nil {
		b := d.Logo.Bounds()
		if iw, ih := float64(b.Dx()), float64(b.Dy()); iw > 0 && ih > 0 {
			s := math.Min(170/iw, 54/ih)
			if err := w.p.ImageFrom(d.Logo, marginL, y, &gopdf.Rect{W: iw * s, H: ih * s}); err != nil && w.err == nil {
				w.err = err
			}
			logoBottom = y + ih*s
		}
	}
	w.font(false, 8.5, cMuted)
	ly := y + 8
	for _, s := range []string{d.Scope, d.Made, d.For} {
		for _, l := range w.wrap(s, contentW*0.55) {
			w.right(pageW-marginR, ly, l)
			ly += 11.5
		}
	}
	y = math.Max(logoBottom, ly) + 30

	w.font(true, 21, cInk)
	for _, l := range w.wrap(d.Title, contentW) {
		y += 19
		w.text(marginL, y, l)
		y += 6
	}
	w.font(false, 11.5, cMuted)
	y += 12
	w.text(marginL, y, d.Period)
	y += 22

	if n := len(d.Tiles); n > 0 {
		const gap, th = 10.0, 66.0
		tw := (contentW - gap*float64(n-1)) / float64(n)
		for i, t := range d.Tiles {
			x := marginL + float64(i)*(tw+gap)
			w.stroke(cTileBorder)
			w.p.SetLineWidth(0.8)
			w.dashed(false)
			w.round(x, y, tw, th, 5, "D")
			w.caps(false, 6.8, cMuted)
			w.text(x+11, y+15, w.fit(strings.ToUpper(t.Label), tw-22))
			w.font(true, 16, toneInk(t.Tone))
			w.text(x+11, y+35, w.fit(t.Value, tw-22))
			w.font(false, 7.2, cMuted)
			for i, l := range w.wrap(t.Sub, tw-22) {
				if i == 2 {
					break
				}
				w.text(x+11, y+49+float64(i)*9, l)
			}
		}
		y += th + 22
	}
	w.y = y
}

func (w *writer) summary(d Doc) {
	if len(d.Hosts) > 0 || d.HostsMore != "" {
		cols := []col{{"Host", 0.22, false}, {"Group", 0.22, false}, {"Uptime", 0.12, true}, {"Downtime", 0.15, true}, {"Problems", 0.13, true}, {"Longest", 0.16, true}}
		rows := make([]row, 0, len(d.Hosts))
		for _, h := range d.Hosts {
			rows = append(rows, row{tint: h.Under, cells: []cell{
				{text: h.Name, bold: true}, {text: h.Group, tone: Muted}, {text: h.Uptime, tone: h.UptimeTone},
				{text: h.Downtime}, {text: h.Problems}, {text: h.Longest},
			}})
		}
		w.heading("Hosts")
		w.table(cols, rows, d.HostsMore)
		w.y += 18
	}
	if len(d.Sensors) > 0 {
		cols := []col{{"Sensor", 0.18, false}, {"Host", 0.15, false}, {"Min", 0.11, true}, {"Avg", 0.11, true}, {"Max", 0.11, true}, {"95th", 0.11, true}, {"In warning", 0.12, true}, {"In error", 0.11, true}}
		rows := make([]row, 0, len(d.Sensors))
		for _, s := range d.Sensors {
			rows = append(rows, row{cells: []cell{
				{text: s.Name, bold: true, wrap: true}, {text: s.Host, tone: Muted, wrap: true}, {text: s.Min}, {text: s.Avg}, {text: s.Max}, {text: s.P95},
				{text: s.InWarn, tone: s.WarnTone, bold: s.WarnTone != Plain}, {text: s.InErr, tone: s.ErrTone, bold: s.ErrTone != Plain},
			}})
		}
		w.heading("Sensors")
		w.table(cols, rows, "")
		w.y += 18
	}
	if d.Footnote != "" {
		w.font(false, 7.5, cMuted)
		for _, l := range w.wrap(d.Footnote, contentW) {
			w.need(11)
			w.y += 10.5
			w.text(marginL, w.y, l)
		}
		w.y += 8
	}
}

func (w *writer) heading(s string) {
	w.need(60)
	w.caps(true, 9.5, cAccent)
	w.y += 10
	w.text(marginL, w.y, strings.ToUpper(s))
	w.y += 9
}

// --- tables ---

type col struct {
	title string
	frac  float64
	right bool
}

type cell struct {
	text, sub string
	bold      bool
	tone      Tone
	dot       Tone // a severity dot before the text (Plain: none)
	wrap      bool // the text wraps instead of being shortened
}

type row struct {
	cells []cell
	tint  bool
}

const (
	cellPad  = 5.0
	rowLine  = 11.0
	tableTxt = 8.6
)

func (w *writer) tableHead(cols []col) {
	w.caps(true, 6.6, cMuted)
	x := marginL
	w.y += 12
	for _, c := range cols {
		cw := c.frac * contentW
		t := strings.ToUpper(c.title)
		if c.right {
			w.right(x+cw-cellPad, w.y, t)
		} else {
			w.text(x+cellPad, w.y, t)
		}
		x += cw
	}
	w.y += 5
	w.dashed(false)
	w.line(marginL, w.y, marginL+contentW, w.y, cRule, 0.9)
}

// table draws rows under a header, carrying the header over to each new page; more is a last,
// muted line for what was left out.
func (w *writer) table(cols []col, rows []row, more string) {
	w.need(40)
	w.tableHead(cols)
	for _, r := range rows {
		// The row's height: its tallest cell (a wrapped text, or a text with a line under it).
		lines := make([][]string, len(r.cells))
		h := 1
		for i, c := range r.cells {
			cw := cols[i].frac*contentW - 2*cellPad
			if c.dot != Plain {
				cw -= 9
			}
			w.font(c.bold, tableTxt, cInk)
			if c.wrap {
				lines[i] = w.wrap(c.text, cw)
				if len(lines[i]) > 4 {
					lines[i] = append(lines[i][:3], w.fit(strings.Join(lines[i][3:], " "), cw))
				}
			} else {
				lines[i] = []string{w.fit(c.text, cw)}
			}
			n := len(lines[i])
			if c.sub != "" {
				n++
			}
			if n > h {
				h = n
			}
		}
		rh := 8 + float64(h)*rowLine
		if w.y+rh > bottom {
			w.page()
			w.tableHead(cols)
		}
		if r.tint {
			w.rect(marginL, w.y+0.5, contentW, rh-1, cTint)
		}
		x := marginL
		for i, c := range r.cells {
			cw := cols[i].frac * contentW
			tx := x + cellPad
			if c.dot != Plain {
				w.fill(toneInk(c.dot))
				w.round(tx, w.y+8.2, 5, 5, 2.5, "F")
				tx += 9
			}
			w.font(c.bold, tableTxt, toneInk(c.tone))
			by := w.y + 4 + rowLine
			for _, l := range lines[i] {
				if cols[i].right {
					w.right(x+cw-cellPad, by, l)
				} else {
					w.text(tx, by, l)
				}
				by += rowLine
			}
			if c.sub != "" {
				w.font(false, 7, cMuted)
				w.text(tx, by-1.5, w.fit(c.sub, cw-2*cellPad-(tx-x-cellPad)))
			}
			x += cw
		}
		w.y += rh
		w.line(marginL, w.y, marginL+contentW, w.y, cFaint, 0.7)
	}
	if more != "" {
		w.need(20)
		w.font(false, 8.2, cMuted)
		w.text(marginL+cellPad, w.y+4+rowLine, more)
		w.y += 8 + rowLine
		w.line(marginL, w.y, marginL+contentW, w.y, cFaint, 0.7)
	}
}

// --- host sections ---

func (w *writer) section(s Section) {
	w.need(130)
	y := w.y + 14
	w.font(true, 15, cInk)
	w.text(marginL, y+13, w.fit(s.Name, contentW*0.7))
	w.font(false, 8, cMuted)
	w.text(marginL, y+26, w.fit(s.Sub, contentW*0.75))
	if s.Uptime != "" {
		w.font(true, 15, toneInk(s.UptimeTone))
		w.right(pageW-marginR, y+13, s.Uptime)
		w.caps(false, 6.4, cMuted)
		w.right(pageW-marginR, y+25, "UPTIME")
	}
	y += 32
	w.dashed(false)
	w.line(marginL, y, marginL+contentW, y, cInk, 1.1)
	y += 6
	if n := len(s.Days); n > 0 {
		gap := 1.6
		if n > 40 {
			gap = 0.8
		}
		cw := (contentW - gap*float64(n-1)) / float64(n)
		for i, d := range s.Days {
			c := cFaint
			switch d.Tone {
			case Good:
				c = cStripGood
			case Warn:
				c = cWarn
			case Err:
				c = cErr
			}
			w.rect(marginL+float64(i)*(cw+gap), y, cw, 10, c)
		}
		y += 10
	}
	w.y = y + 6
	w.sec = s.Name
	defer func() { w.sec = "" }()
	switch {
	case len(s.Problems) > 0:
		cols := []col{{"Started", 0.15, false}, {"Lasted", 0.13, true}, {"Problem", 0.42, false}, {"Acknowledged", 0.3, false}}
		rows := make([]row, 0, len(s.Problems))
		for _, p := range s.Problems {
			ack := cell{text: p.Ack, tone: Muted, wrap: true}
			if p.Ack == "" {
				ack.text = "-"
			}
			rows = append(rows, row{cells: []cell{
				{text: p.Started}, {text: p.Lasted, wrap: true}, {text: p.Name, sub: p.Sensor, dot: p.Tone, wrap: true}, ack,
			}})
		}
		w.table(cols, rows, s.ProblemsMore)
		w.y += 10
	case s.NoProblems != "":
		w.font(false, 8.5, cMuted)
		w.y += 12
		w.text(marginL, w.y, s.NoProblems)
		w.y += 8
	}
	for _, c := range s.Charts {
		w.chart(c)
	}
	w.y += 6
}

// --- charts ---

const gutter = 48.0 // the y axis labels and threshold tags

func (w *writer) chart(c Chart) {
	plotH := 104.0
	switch {
	case c.Kind == UpDownChart:
		plotH = 30
	case c.Compact:
		plotH = 64
	}
	legend := len(c.Series) > 1
	total := 16 + plotH + 14 + 10
	if legend || c.Foot != "" {
		total += 13
	}
	if len(c.Series) == 0 && len(c.Down) == 0 { // nothing to draw: its title, and why
		w.need(30)
		y := w.y + 12
		w.font(true, 9.5, cInk)
		w.text(marginL, y, w.fit(c.Title, contentW*0.42))
		w.font(false, 7.5, cMuted)
		w.right(pageW-marginR, y, c.Foot)
		w.y = y + 10
		return
	}
	w.need(total)
	y := w.y + 12
	w.font(true, 9.5, cInk)
	w.text(marginL, y, w.fit(c.Title, contentW*0.42))
	x := pageW - marginR
	for i := len(c.Stats) - 1; i >= 0; i-- {
		st := c.Stats[i]
		w.font(false, 7.5, cMuted)
		w.right(x, y, st.Value)
		x -= w.width(st.Value) + 3
		w.font(true, 7.5, cInk)
		w.right(x, y, st.Label)
		x -= w.width(st.Label) + 9
	}
	y += 8
	px0, px1 := marginL+gutter, pageW-marginR
	py0, py1 := y+4, y+4+plotH
	if c.Kind == UpDownChart {
		w.upDown(c, px0, px1, py0, py1)
	} else {
		w.lines(c, px0, px1, py0, py1)
	}
	w.timeAxis(c, px0, px1, py1)
	y = py1 + 14
	if legend || c.Foot != "" {
		y += 11
		lx := marginL
		if legend {
			for i, s := range c.Series {
				col := seriesColors[i%len(seriesColors)]
				w.dashed(false)
				w.line(lx, y-2.6, lx+9, y-2.6, col, 1.6)
				w.font(false, 7.2, cMuted)
				w.text(lx+12, y, s.Name)
				lx += 12 + w.width(s.Name) + 10
			}
		}
		w.font(false, 7.2, cMuted)
		w.text(lx, y, c.Foot)
	}
	w.y = y + 6
}

// scale works out a chart's value range: from 0 (or the lowest value, when it goes below 0) to a
// round number a little past the highest value (never past the chart's ceiling, 100 for a
// percentage), taking in a threshold that is no more than twice that value away (one further off
// would flatten the line; it is left out).
func scale(c Chart) (lo, hi float64, thr []Threshold) {
	first := true
	for _, s := range c.Series {
		for _, p := range s.Points {
			if first || p.V < lo {
				lo = p.V
			}
			if first || p.V > hi {
				hi = p.V
			}
			first = false
		}
	}
	dataHi := hi
	if lo > 0 {
		lo = 0
	}
	for _, t := range c.Thresholds {
		if t.V >= lo && t.V <= math.Max(hi*2, hi+1e-9) {
			thr = append(thr, t)
			if t.V > hi {
				hi = t.V
			}
		}
	}
	hi = niceCeil(hi*1.08, c.Base1024) // a little room above the highest value
	// The ceiling caps the headroom, never the data: a percentage that stays under 100 tops out
	// at 100, one that went past it (a VM's CPU at 112 %) keeps the axis that shows it.
	if c.Ceil > 0 && hi > c.Ceil && dataHi <= c.Ceil {
		hi = math.Max(c.Ceil, niceCeil(math.Max(lo, 0), c.Base1024))
	}
	if lo < 0 {
		lo = -niceCeil(-lo, c.Base1024)
	}
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi, thr
}

// niceCeil rounds v up to a round number: 1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6 or 8 times a power of ten
// (of 1024 first, for bytes), so a little room above 100 is 120, not 200.
func niceCeil(v float64, base1024 bool) float64 {
	if v <= 0 {
		return 0
	}
	k := 1.0
	if base1024 {
		for v/k >= 1024 {
			k *= 1024
		}
	}
	m := v / k
	p := math.Pow(10, math.Floor(math.Log10(m)))
	for _, f := range []float64{1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if f*p >= m*0.9999999 {
			if base1024 && f*p >= 1000 {
				return 1024 * k // a round 1 GiB, not 1000 MiB
			}
			return f * p * k
		}
	}
	return 10 * p * k
}

// decimate keeps a long series drawable: past n points it keeps each bucket's lowest and highest
// value, in time order, so a spike survives.
func decimate(pts []Point, n int) []Point {
	if len(pts) <= n || n < 4 {
		return pts
	}
	per := float64(len(pts)) / float64(n/2)
	out := make([]Point, 0, n+2)
	for b := 0.0; int(b) < len(pts); b += per {
		end := int(b + per)
		if end > len(pts) {
			end = len(pts)
		}
		bucket := pts[int(b):end]
		if len(bucket) == 0 {
			continue
		}
		lo, hi := 0, 0
		for i, p := range bucket {
			if p.V < bucket[lo].V {
				lo = i
			}
			if p.V > bucket[hi].V {
				hi = i
			}
		}
		if lo > hi {
			lo, hi = hi, lo
		}
		out = append(out, bucket[lo])
		if hi != lo {
			out = append(out, bucket[hi])
		}
	}
	return out
}

func (w *writer) lines(c Chart, px0, px1, py0, py1 float64) {
	lo, hi, thr := scale(c)
	span := c.To.Sub(c.From).Seconds()
	if span <= 0 {
		span = 1
	}
	xAt := func(t time.Time) float64 { return px0 + (px1-px0)*t.Sub(c.From).Seconds()/span }
	yAt := func(v float64) float64 { return py1 - (py1-py0)*(v-lo)/(hi-lo) }

	// The threshold tags first, so the axis values they would cover are left out.
	type tag struct {
		y    float64
		t    Threshold
		text string
	}
	var tags []tag
	for _, t := range thr {
		tags = append(tags, tag{yAt(t.V), t, label(c, t.V)})
	}
	near := func(y float64) bool {
		for _, t := range tags {
			if math.Abs(t.y-y) < 9 {
				return true
			}
		}
		return false
	}
	// The time it was down: shaded across the plot over the fill, and marked in red along its foot
	// over everything, so an outage of minutes in a month still shows.
	downAt := func(d Span) (float64, float64) {
		x0, x1 := math.Max(xAt(d.From), px0), math.Min(xAt(d.To), px1)
		if x1-x0 < 1.6 {
			m := (x0 + x1) / 2
			x0, x1 = m-0.8, m+0.8
		}
		return x0, x1
	}
	w.dashed(false)
	for _, v := range []float64{lo, (lo + hi) / 2, hi} {
		y := yAt(v)
		w.line(px0, y, px1, y, cFaint, 0.6)
		if !near(y) {
			w.font(false, 6.8, cMuted)
			w.right(px0-5, y+2.4, label(c, v))
		}
	}

	single := len(c.Series) == 1
	// A single channel gets the light fill under it, run by run.
	if single {
		if pts := decimate(c.Series[0].Points, 900); len(pts) > 0 {
			step := medianStep(pts)
			w.fill(cFill)
			run := []gopdf.Point{}
			flush := func() {
				if len(run) >= 2 {
					poly := append([]gopdf.Point{{X: run[0].X, Y: py1}}, run...)
					poly = append(poly, gopdf.Point{X: run[len(run)-1].X, Y: py1})
					w.p.Polygon(poly, "F")
				}
				run = run[:0]
			}
			for i, p := range pts {
				if i > 0 && p.T.Sub(pts[i-1].T) > step*5/2 {
					flush()
				}
				run = append(run, gopdf.Point{X: xAt(p.T), Y: yAt(p.V)})
			}
			flush()
		}
	}
	for _, d := range c.Down {
		x0, x1 := downAt(d)
		w.rect(x0, py0, x1-x0, py1-py0, cDownShade)
	}
	for si, s := range c.Series {
		pts := decimate(s.Points, 900)
		if len(pts) == 0 {
			continue
		}
		step := medianStep(pts)
		col := seriesColors[si%len(seriesColors)]
		w.dashed(false)
		w.p.SetLineWidth(1)
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			if b.T.Sub(a.T) > step*5/2 {
				continue
			}
			if !single || len(thr) == 0 {
				w.stroke(col)
				w.p.Line(xAt(a.T), yAt(a.V), xAt(b.T), yAt(b.V))
				continue
			}
			// Coloured by value: the stretch past a threshold takes its colour.
			cuts := []float64{0, 1}
			for _, t := range thr {
				if (a.V-t.V)*(b.V-t.V) < 0 {
					cuts = append(cuts, (t.V-a.V)/(b.V-a.V))
				}
			}
			sortFloats(cuts)
			for k := 1; k < len(cuts); k++ {
				f0, f1 := cuts[k-1], cuts[k]
				if f1-f0 < 1e-9 {
					continue
				}
				mid := a.V + (b.V-a.V)*(f0+f1)/2
				w.stroke(band(mid, thr, c.Below, col))
				ax, ay := xAt(a.T), yAt(a.V)
				bx, by := xAt(b.T), yAt(b.V)
				w.p.Line(ax+(bx-ax)*f0, ay+(by-ay)*f0, ax+(bx-ax)*f1, ay+(by-ay)*f1)
			}
		}
		if len(pts) == 1 { // a lone value: a dot, so it shows
			w.fill(col)
			x, y := xAt(pts[0].T), yAt(pts[0].V)
			w.round(x-1.5, y-1.5, 3, 3, 1.5, "F")
		}
	}

	for _, t := range tags {
		bg, ink := cWarn, cWarnInk
		if t.t.Tone == Err {
			bg, ink = cErr, cWhite
		}
		w.dashed(true)
		w.line(px0, t.y, px1, t.y, bg, 0.8)
		w.dashed(false)
		w.font(true, 6.2, ink)
		tw := w.width(t.text) + 6
		w.fill(bg)
		w.round(px0-4-tw, t.y-4.8, tw, 9.6, 2, "F")
		w.text(px0-4-tw+3, t.y+2.2, t.text)
	}
	for _, d := range c.Down {
		x0, x1 := downAt(d)
		w.rect(x0, py1-3, x1-x0, 3, cErr)
	}
}

// band picks a value's colour against the thresholds: an error one past, a warning one, or the
// channel's own.
func band(v float64, thr []Threshold, below bool, base rgb) rgb {
	worst := Plain
	for _, t := range thr {
		past := v >= t.V
		if below {
			past = v <= t.V
		}
		if past && t.Tone > worst {
			worst = t.Tone
		}
	}
	switch worst {
	case Err:
		return cErr
	case Warn:
		return cWarn
	}
	return base
}

func (w *writer) upDown(c Chart, px0, px1, py0, py1 float64) {
	span := c.To.Sub(c.From).Seconds()
	if span <= 0 {
		span = 1
	}
	xAt := func(t time.Time) float64 { return px0 + (px1-px0)*t.Sub(c.From).Seconds()/span }
	w.dashed(false)
	w.line(px0, py1, px1, py1, cFaint, 0.6)
	w.font(false, 6.8, cMuted)
	w.right(px0-5, py0+2.4, "up")
	w.right(px0-5, py1+2.4, "down")
	if len(c.Series) > 0 {
		pts := c.Series[0].Points
		step := medianStep(pts)
		w.p.SetLineWidth(1.1)
		w.stroke(seriesColors[0])
		var runFrom time.Time
		for i, p := range pts {
			if i == 0 || p.T.Sub(pts[i-1].T) > step*5/2 {
				if i > 0 {
					w.p.Line(xAt(runFrom), py0, xAt(pts[i-1].T), py0)
				}
				runFrom = p.T
			}
		}
		if len(pts) > 0 {
			w.p.Line(xAt(runFrom), py0, xAt(pts[len(pts)-1].T), py0)
		}
	}
	for _, d := range c.Down {
		x0, x1 := xAt(d.From), xAt(d.To)
		if x1-x0 < 1.6 {
			m := (x0 + x1) / 2
			x0, x1 = m-0.8, m+0.8
		}
		w.rect(math.Max(x0, px0), py0-2, math.Min(x1, px1)-math.Max(x0, px0), py1-py0+2, cErr)
	}
}

// timeAxis labels the bottom of a chart: hours over a day or two, days over a few months, months
// past that.
func (w *writer) timeAxis(c Chart, px0, px1, py1 float64) {
	loc := c.Loc
	if loc == nil {
		loc = time.Local
	}
	span := c.To.Sub(c.From)
	if span <= 0 {
		return
	}
	xAt := func(t time.Time) float64 { return px0 + (px1-px0)*t.Sub(c.From).Seconds()/span.Seconds() }
	from := c.From.In(loc)
	var ticks []time.Time
	var layout string
	switch days := span.Hours() / 24; {
	case days <= 2.5:
		// The shortest round step that gives at most 7 ticks: 5 minutes for a close-up of an outage,
		// hours over a day or two.
		step := 12 * time.Hour
		for _, s := range []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour} {
			if span/s <= 7 {
				step = s
				break
			}
		}
		midnight := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
		t := midnight.Add(from.Sub(midnight) / step * step)
		for ; !t.After(c.To); t = t.Add(step) {
			ticks = append(ticks, t)
		}
		layout = "15:04"
	case days <= 120:
		step := int(math.Ceil(days / 5))
		if step > 7 {
			step = (step + 6) / 7 * 7
		}
		t := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
		for ; !t.After(c.To); t = t.AddDate(0, 0, step) {
			ticks = append(ticks, t)
		}
		layout = "Jan 2"
	default:
		months := int(math.Ceil(days / 30 / 6))
		t := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, loc)
		for ; !t.After(c.To); t = t.AddDate(0, months, 0) {
			ticks = append(ticks, t)
		}
		layout = "Jan 2006"
	}
	w.font(false, 6.8, cMuted)
	lastRight := -1.0
	for _, t := range ticks {
		if t.Before(c.From) {
			continue
		}
		s := t.Format(layout)
		tw := w.width(s)
		x := math.Min(math.Max(xAt(t)-tw/2, px0), px1-tw)
		if x < lastRight+6 {
			continue
		}
		w.text(x, py1+10, s)
		lastRight = x + tw
	}
}

func label(c Chart, v float64) string {
	if c.Label != nil {
		return c.Label(v)
	}
	return trim(v)
}

func trim(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconvF(v), "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

func medianStep(pts []Point) time.Duration {
	if len(pts) < 2 {
		return time.Hour
	}
	d := make([]float64, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		d = append(d, float64(pts[i].T.Sub(pts[i-1].T)))
	}
	sortFloats(d)
	m := time.Duration(d[len(d)/2])
	if m <= 0 {
		return time.Hour
	}
	return m
}

// --- footer ---

func (w *writer) footer(d Doc, i, n int) {
	y := pageH - 34.0
	w.dashed(false)
	w.line(marginL, y, marginL+contentW, y, cFaint, 0.7)
	w.font(false, 7, cMuted)
	w.text(marginL, y+12, w.fit(d.Title+" - "+d.Period, contentW*0.65))
	w.right(pageW-marginR, y+12, "Argus - page "+itoa(i)+" of "+itoa(n))
}

func strconvF(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
func itoa(n int) string         { return strconv.Itoa(n) }
func sortFloats(f []float64)    { sort.Float64s(f) }
