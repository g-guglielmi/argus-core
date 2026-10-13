// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // logos
	_ "image/png"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/notify"
	"argus/internal/provision"
	"argus/internal/report"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// A report is a PDF of the hosts and sensors someone picked, over a period: page 1 sums up the
// uptime, downtime and problems and the sensors' figures, then each host gets a section with its
// day strip, its problems and its sensors' charts (internal/report draws it). This file works the
// figures out; reportsapi.go serves, saves, emails and schedules reports.

const (
	reportMaxDays      = 366
	reportIncidentRows = 5000
	// A problem that started up to a month before the period and ran into it is counted too.
	reportLookback    = 31 * 24 * time.Hour
	reportFoldHosts   = 12 // more hosts than this and the clean ones fold into one line
	reportProblemRows = 40 // per host
	reportMaxSensors  = 60
	reportHostCharts  = 100 // hosts whose availability is charted, at most
	reportCloseUps    = 3   // outages drawn up close per host, the longest first
	reportCloseUpsAll = 60  // and in a report
	reportHistoryDays = 3   // up to this long, charts read raw history; longer, hourly trends
	reportLogoKey     = "report:logo"
)

// reportRun is one making of a report: for whom (their scope), when, and to whom it is sent.
type reportRun struct {
	name  string
	spec  store.ReportSpec
	scope siteScope
	now   time.Time
	to    []string
}

// reportPeriod is the time a report covers, made at now: whole days for the last 7 or 30 days and
// the last calendar month, up to now for this month, and the days picked for a custom period.
func reportPeriod(spec store.ReportSpec, now time.Time, loc *time.Location) (from, to time.Time, label string, err error) {
	today := startOfDay(now, loc)
	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	switch spec.Period {
	case "", "7d":
		from, to = today.AddDate(0, 0, -7), today
	case "30d":
		from, to = today.AddDate(0, 0, -30), today
	case "last_month":
		from, to = month.AddDate(0, -1, 0), month
	case "this_month":
		from, to = month, now
	case "custom":
		f, e1 := time.ParseInLocation("2006-01-02", spec.From, loc)
		t, e2 := time.ParseInLocation("2006-01-02", spec.To, loc)
		if e1 != nil || e2 != nil {
			return from, to, "", errors.New("pick the first and the last day of the period")
		}
		if t.Before(f) {
			return from, to, "", errors.New("the last day is before the first")
		}
		from, to = f, t.AddDate(0, 0, 1)
		if to.After(now) {
			to = now
		}
	default:
		return from, to, "", fmt.Errorf("unknown period %q", spec.Period)
	}
	if !from.Before(to) {
		return from, to, "", errors.New("the period hasn't started yet")
	}
	if to.Sub(from) > reportMaxDays*24*time.Hour {
		return from, to, "", errors.New("a report covers a year at most")
	}
	return from, to, periodLabel(from.In(loc), to.Add(-time.Second).In(loc)), nil
}

// periodLabel reads a span of days: "12 October 2026", "1 to 30 September 2026", "28 September to
// 4 October 2026", "28 December 2025 to 3 January 2026".
func periodLabel(a, b time.Time) string {
	switch {
	case a.Year() == b.Year() && a.YearDay() == b.YearDay():
		return a.Format("2 January 2006")
	case a.Year() == b.Year() && a.Month() == b.Month():
		return a.Format("2") + " to " + b.Format("2 January 2006")
	case a.Year() == b.Year():
		return a.Format("2 January") + " to " + b.Format("2 January 2006")
	}
	return a.Format("2 January 2006") + " to " + b.Format("2 January 2006")
}

// fmtSpan reads a stretch of time the way a report does: "0", "40 s", "9 min", "1 h 35 min",
// "3 d 4 h".
func fmtSpan(d time.Duration) string {
	switch {
	case d <= 0:
		return "0"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return strconv.Itoa(h) + " h"
		}
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	if h == 0 {
		return strconv.Itoa(days) + " d"
	}
	return strconv.Itoa(days) + " d " + strconv.Itoa(h) + " h"
}

// fmtPct reads an uptime with two decimals, rounded down: 99.999 is "99.99 %", never "100 %", which
// is kept for no downtime at all.
func fmtPct(p float64) string {
	if p >= 100 {
		return "100 %"
	}
	return strconv.FormatFloat(math.Floor(p*100)/100, 'f', -1, 64) + " %"
}

// pctTone colours an uptime: all of it green, at or over the target amber, under it red (with no
// target, 99 % is the line).
func pctTone(p, target float64) report.Tone {
	if target <= 0 {
		target = 99
	}
	switch {
	case p >= 100:
		return report.Good
	case p >= target:
		return report.Warn
	}
	return report.Err
}

// occurrencesBetween is every stretch a maintenance window ran within [from, to).
func occurrencesBetween(w store.MaintenanceWindow, from, to time.Time, loc *time.Location) []report.Span {
	dur := time.Duration(w.DurationMin) * time.Minute
	var out []report.Span
	add := func(s time.Time) {
		if e := s.Add(dur); e.After(from) && s.Before(to) {
			out = append(out, report.Span{From: s, To: e})
		}
	}
	if w.Kind == "once" {
		if w.StartAt > 0 {
			add(time.Unix(w.StartAt, 0))
		}
		return out
	}
	first := from.In(loc).Add(-dur)
	for day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc); day.Before(to); day = day.AddDate(0, 0, 1) {
		if runsOn(w, day) {
			add(time.Date(day.Year(), day.Month(), day.Day(), w.Minute/60, w.Minute%60, 0, 0, loc))
		}
	}
	return out
}

func overlapsAny(spans []report.Span, a, b time.Time) bool {
	for _, s := range spans {
		if s.From.Before(b) && s.To.After(a) {
			return true
		}
	}
	return false
}

// clipped is how much of [a, b) lies inside [from, to).
func clipped(a, b, from, to time.Time) time.Duration {
	if a.Before(from) {
		a = from
	}
	if b.After(to) {
		b = to
	}
	if !b.After(a) {
		return 0
	}
	return b.Sub(a)
}

type trendHour struct {
	at  time.Time
	num int64
	avg float64
}

// trendHours reads the hourly trends of up/down items within [from, to), by item, each hour exact
// where the raw history still has its checks (exactUptime); stale counts the hours it hasn't.
func (s *Server) trendHours(ctx context.Context, ids []string, from, to time.Time) (out map[string][]trendHour, stale int, err error) {
	out = map[string][]trendHour{}
	valueType := map[string]string{}
	if its, err := s.zbx.ItemsByIDs(ctx, ids); err == nil {
		for id, it := range its {
			valueType[id] = it.ValueType
		}
	}
	for start := 0; start < len(ids); start += uptimeBatchItems {
		chunk := ids[start:min(start+uptimeBatchItems, len(ids))]
		rows, err := s.zbx.TrendRows(ctx, chunk, from.Unix(), to.Unix()-1)
		if err != nil {
			return out, stale, err
		}
		rows, n := s.exactUptime(ctx, rows, valueType)
		stale += n
		for _, r := range rows {
			avg, err := strconv.ParseFloat(r.ValueAvg, 64)
			if err != nil {
				continue
			}
			out[r.ItemID] = append(out[r.ItemID], trendHour{at: time.Unix(atoi64(r.Clock), 0), num: atoi64(r.Num), avg: avg})
		}
	}
	return out, stale, nil
}

// series is a sensor channel's values over the period, and its figures.
type series struct {
	pts                []report.Point
	min, avg, max, p95 float64
	step               time.Duration
}

// readSeries reads a channel's values: raw history over a short period, hourly trends (each hour's
// average, with min and max from the hours' extremes) over a longer one.
func (s *Server) readSeries(ctx context.Context, it zabbix.Item, from, to time.Time) (series, bool) {
	var out series
	var vals []float64
	if to.Sub(from) <= reportHistoryDays*24*time.Hour {
		hist, err := s.zbx.History(ctx, it.ItemID, atoi(it.ValueType), from.Unix(), to.Unix()-1)
		if err != nil {
			return out, false
		}
		for _, h := range hist {
			v, err := strconv.ParseFloat(h.Value, 64)
			if err != nil {
				continue
			}
			out.pts = append(out.pts, report.Point{T: time.Unix(atoi64(h.Clock), 0), V: v})
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return out, false
		}
		out.min, out.max = vals[0], vals[0]
		for _, v := range vals {
			out.min, out.max = math.Min(out.min, v), math.Max(out.max, v)
		}
	} else {
		trends, err := s.zbx.Trends(ctx, it.ItemID, from.Unix(), to.Unix()-1)
		if err != nil {
			return out, false
		}
		sort.Slice(trends, func(i, j int) bool { return atoi64(trends[i].Clock) < atoi64(trends[j].Clock) })
		if isUpDownKey(it.Key) && it.ValueType == "3" {
			rows := make([]zabbix.TrendRow, len(trends))
			for i, t := range trends {
				rows[i] = zabbix.TrendRow{ItemID: it.ItemID, Clock: t.Clock, Num: "1", ValueAvg: t.ValueAvg}
			}
			rows, _ = s.exactUptime(ctx, rows, map[string]string{it.ItemID: "3"})
			for i := range trends {
				if rows[i].ValueAvg != trends[i].ValueAvg {
					trends[i].ValueAvg, trends[i].ValueMin = rows[i].ValueAvg, "0"
				}
			}
		}
		first := true
		for _, t := range trends {
			avg, e1 := strconv.ParseFloat(t.ValueAvg, 64)
			lo, e2 := strconv.ParseFloat(t.ValueMin, 64)
			hi, e3 := strconv.ParseFloat(t.ValueMax, 64)
			if e1 != nil || e2 != nil || e3 != nil {
				continue
			}
			out.pts = append(out.pts, report.Point{T: time.Unix(atoi64(t.Clock), 0).Add(30 * time.Minute), V: avg})
			vals = append(vals, avg)
			if first || lo < out.min {
				out.min = lo
			}
			if first || hi > out.max {
				out.max = hi
			}
			first = false
		}
		if len(vals) == 0 {
			return out, false
		}
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	out.avg = sum / float64(len(vals))
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	out.p95 = sorted[int(math.Ceil(0.95*float64(len(sorted))))-1]
	out.step = time.Hour
	if len(out.pts) > 1 {
		out.step = to.Sub(from) / time.Duration(len(out.pts))
	}
	return out, true
}

// leadRank orders a group's channels for the one it leads with, as the host page does.
func leadRank(channel string) int {
	for i, c := range []string{"Response time", "Used %", "In", "Download", "Clients", "Since last run", "Total"} {
		if channel == c || strings.HasPrefix(channel, c+" ") {
			return i
		}
	}
	return 99
}

// downOf is when an up/down channel read down: each value under 1 takes its share of its step.
func downOf(ss series) ([]report.Span, time.Duration) {
	var spans []report.Span
	var total time.Duration
	step := ss.step
	if len(ss.pts) > 1 {
		step = medianGap(ss.pts)
	}
	for _, p := range ss.pts {
		if p.V >= 0.999 {
			continue
		}
		d := time.Duration((1 - math.Max(0, math.Min(1, p.V))) * float64(step))
		start := p.T
		if step >= time.Hour {
			start = p.T.Add(-30 * time.Minute) // a trend point sits mid-hour
		}
		spans = append(spans, report.Span{From: start, To: start.Add(d)})
		total += d
	}
	return spans, total
}

func medianGap(pts []report.Point) time.Duration {
	g := make([]time.Duration, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		g = append(g, pts[i].T.Sub(pts[i-1].T))
	}
	sort.Slice(g, func(i, j int) bool { return g[i] < g[j] })
	return g[len(g)/2]
}

// reportLogo is the logo set in Settings for the reports' first page (nil: none).
func (s *Server) reportLogo(ctx context.Context) image.Image {
	v, ok, err := s.st.MetaGet(ctx, reportLogoKey)
	if err != nil || !ok || v == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return img
}

// reportHost is a host in a report: covered (its uptime and problems) and/or with sensors picked.
type reportHost struct {
	h       zabbix.Host
	groups  []string
	covered bool
	sensors []store.ReportSensor
	// worked out
	item    string // the uptime sensor
	up      upAgg
	days    map[string]upAgg
	down    time.Duration
	maint   []report.Span
	maintIn time.Duration // the maintenance time left out
	probs   []incidentView
	longest time.Duration
}

func (h *reportHost) pct() (float64, bool) { return aggPct(h.up) }

// aggPct is an uptime in percent: 100 only when no check found it down (a rounded 99.9996 stays
// under 100), false without a single check.
func aggPct(a upAgg) (float64, bool) {
	p := a.pct()
	if p == nil {
		return 0, false
	}
	if a.up >= float64(a.num) {
		return 100, true
	}
	return math.Min(*p, 99.999), true
}

// buildReport works a report out for one run.
func (s *Server) buildReport(ctx context.Context, run reportRun) (report.Doc, error) {
	spec := run.spec
	loc := s.mgr.Location()
	from, to, period, err := reportPeriod(spec, run.now, loc)
	if err != nil {
		return report.Doc{}, err
	}
	all, err := s.zbx.Hosts(ctx)
	if err != nil {
		return report.Doc{}, fmt.Errorf("Zabbix: %w", err)
	}
	classes, _ := s.st.DeviceClasses(ctx)
	ips, _ := s.zbx.HostIPs(ctx)

	picked := map[string]bool{}
	for _, id := range spec.Hosts {
		picked[id] = true
	}
	byID := map[string]*reportHost{}
	for _, h := range all {
		groups := make([]string, 0, len(h.Groups))
		for _, g := range h.Groups {
			groups = append(groups, g.Name)
		}
		if !run.scope.sees(groups) {
			continue
		}
		covered := picked[h.HostID]
		// A site covers its devices; Argus's own probe host only when picked by name.
		if c, _ := provision.ClassByID(classes[h.HostID]); !covered && !c.Internal {
			for _, site := range spec.Sites {
				for _, g := range groups {
					if siteCovers(site, g) {
						covered = true
					}
				}
			}
		}
		byID[h.HostID] = &reportHost{h: h, groups: groups, covered: covered, days: map[string]upAgg{}}
	}
	nSensors := 0
	for _, sn := range spec.Sensors {
		if h := byID[sn.HostID]; h != nil && nSensors < reportMaxSensors {
			h.sensors = append(h.sensors, sn)
			nSensors++
		}
	}
	var hosts []*reportHost
	var covered, ids []string
	for id, h := range byID {
		if h.covered || len(h.sensors) > 0 {
			hosts = append(hosts, h)
			ids = append(ids, id)
		}
		if h.covered {
			covered = append(covered, id)
		}
	}
	if len(hosts) == 0 {
		return report.Doc{}, errors.New("nothing to report on: pick a site, a host or a sensor you can see")
	}
	sort.Slice(hosts, func(i, j int) bool {
		a, b := hosts[i], hosts[j]
		if sa, sb := strings.ToLower(siteOf(a.groups)), strings.ToLower(siteOf(b.groups)); sa != sb {
			return sa < sb
		}
		return strings.ToLower(a.h.Name) < strings.ToLower(b.h.Name)
	})

	// Uptime: each covered host's availability sensor, hour by hour, without the hours a maintenance
	// window covered when the report leaves them out.
	var windows []store.MaintenanceWindow
	if spec.SkipMaintenance {
		windows, _ = s.st.MaintenanceWindows(ctx)
	}
	itemOf := s.hostUptimeItems(ctx, covered)
	upIDs := make([]string, 0, len(itemOf))
	for _, id := range itemOf {
		upIDs = append(upIDs, id)
	}
	hours, staleHours, upErr := s.trendHours(ctx, upIDs, from, to)
	for _, h := range hosts {
		if !h.covered {
			continue
		}
		for _, w := range windows {
			if w.Enabled && windowCovers(w, h.h.HostID, h.groups) {
				h.maint = append(h.maint, occurrencesBetween(w, from, to, loc)...)
			}
		}
		h.item = itemOf[h.h.HostID]
		for _, tr := range hours[h.item] {
			if overlapsAny(h.maint, tr.at, tr.at.Add(time.Hour)) {
				h.maintIn += time.Hour
				continue
			}
			h.up.add(tr.avg, tr.num)
			k := dayKey(tr.at, loc)
			d := h.days[k]
			d.add(tr.avg, tr.num)
			h.days[k] = d
			if tr.num > 0 {
				h.down += time.Duration((1 - math.Max(0, math.Min(1, tr.avg))) * float64(time.Hour))
			}
		}
	}

	// Problems: those that overlap the period, on every host in the report.
	incs, incErr := s.collectIncidentsTill(ctx, ids, nil, from.Add(-reportLookback).Unix(), to.Unix(), reportIncidentRows)
	end := func(v incidentView) time.Time {
		if v.End == 0 {
			return run.now
		}
		return time.Unix(v.End, 0)
	}
	var inPeriod []incidentView
	for _, v := range incs {
		if time.Unix(v.Start, 0).Before(to) && end(v).After(from) {
			inPeriod = append(inPeriod, v)
		}
	}
	sort.Slice(inPeriod, func(i, j int) bool { return inPeriod[i].Start < inPeriod[j].Start })
	errorsN, warningsN := 0, 0
	for _, v := range inPeriod {
		h := byID[v.HostID]
		if h == nil {
			continue
		}
		h.probs = append(h.probs, v)
		if d := clipped(time.Unix(v.Start, 0), end(v), from, to); d > h.longest {
			h.longest = d
		}
		if h.covered {
			if v.Severity >= 3 {
				errorsN++
			} else {
				warningsN++
			}
		}
	}

	// Each covered host's availability chart: its ping's response time with the time it was down
	// shaded (a collector's flag alone when it has no ping), unless the report leaves them out or a
	// sensor picked on the host already is that sensor.
	hostCharts := map[string]store.ReportSensor{}
	rtt := map[string]string{} // host -> its ping's response time
	if !spec.SkipHostCharts || !spec.SkipCloseUps {
		var pinged []string
		for _, h := range hosts {
			if h.covered && h.item != "" {
				pinged = append(pinged, h.h.HostID)
			}
		}
		if len(pinged) > 0 {
			if its, err := s.zbx.HostItemsByKeys(ctx, pinged, []string{"icmppingsec"}); err == nil {
				for _, it := range its {
					if it.Status == "0" {
						rtt[it.HostID] = it.ItemID
					}
				}
			}
		}
	}
	if !spec.SkipHostCharts {
		for _, h := range hosts {
			if !h.covered || h.item == "" || len(hostCharts) == reportHostCharts {
				continue
			}
			if slices.ContainsFunc(h.sensors, func(sn store.ReportSensor) bool { return slices.Contains(sn.Items, h.item) }) {
				continue
			}
			sn := store.ReportSensor{HostID: h.h.HostID, Items: []string{h.item}}
			if id := rtt[h.h.HostID]; id != "" {
				sn.Items = append(sn.Items, id)
			}
			hostCharts[h.h.HostID] = sn
		}
	}

	// Sensors: their items, thresholds and values.
	var itemIDs []string
	for _, h := range hosts {
		for _, sn := range h.sensors {
			itemIDs = append(itemIDs, sn.Items...)
		}
		itemIDs = append(itemIDs, hostCharts[h.h.HostID].Items...)
		if !spec.SkipCloseUps && h.covered && h.item != "" {
			itemIDs = append(itemIDs, h.item)
			if id := rtt[h.h.HostID]; id != "" {
				itemIDs = append(itemIDs, id)
			}
		}
	}
	items := map[string]zabbix.Item{}
	trigs := map[string][]zabbix.ItemTrigger{}
	if len(itemIDs) > 0 {
		items, _ = s.zbx.ItemsByIDs(ctx, itemIDs)
		trigs, _ = s.zbx.ItemTriggers(ctx, itemIDs)
	}

	doc := report.Doc{
		Title:  run.name,
		Period: period,
		Made:   "Made " + fmtStamp(run.now, loc, s.mgr.Clock24h()) + " (" + loc.String() + ")",
		Logo:   s.reportLogo(ctx),
	}
	if len(run.to) > 0 {
		doc.For = "for " + strings.Join(run.to, ", ")
	}
	doc.Scope = reportScopeLine(spec, len(covered), nSensors)

	// Page 1: the tiles and the hosts table.
	var tot upAgg
	var totDown, totMaint time.Duration
	downHosts := 0
	var under []string
	for _, h := range hosts {
		if !h.covered {
			continue
		}
		tot.merge(h.up)
		totDown += h.down
		totMaint += h.maintIn
		if h.down > 0 {
			downHosts++
		}
		if p, ok := h.pct(); ok && spec.Target > 0 && p < spec.Target {
			under = append(under, h.h.Name+", "+fmtPct(p))
		}
	}
	if len(covered) > 0 {
		upTile := report.Tile{Label: "Uptime", Value: "-", Sub: plural(len(covered), "host", "hosts")}
		if p, ok := aggPct(tot); ok {
			upTile.Value = fmtPct(p)
		}
		if spec.Target > 0 {
			upTile.Sub += ", target " + strconv.FormatFloat(spec.Target, 'f', -1, 64) + " %"
		}
		doc.Tiles = append(doc.Tiles, upTile)
		if spec.Target > 0 {
			t := report.Tile{Label: "Under target", Value: "none", Tone: report.Good, Sub: "every host at " + strconv.FormatFloat(spec.Target, 'f', -1, 64) + " % or more"}
			if len(under) > 0 {
				t = report.Tile{Label: "Under target", Value: plural(len(under), "host", "hosts"), Tone: report.Err, Sub: strings.Join(under, "; ")}
			}
			doc.Tiles = append(doc.Tiles, t)
		}
		dt := report.Tile{Label: "Downtime", Value: fmtSpan(totDown), Sub: "no host was down"}
		if downHosts > 0 {
			dt.Sub = plural(downHosts, "host", "hosts")
		}
		if spec.SkipMaintenance {
			dt.Sub += "; maintenance left out"
		}
		doc.Tiles = append(doc.Tiles, dt)
		doc.Tiles = append(doc.Tiles, report.Tile{Label: "Problems", Value: strconv.Itoa(errorsN + warningsN),
			Sub: plural(errorsN, "error", "errors") + ", " + plural(warningsN, "warning", "warnings")})

		clean := 0
		for _, h := range hosts {
			if !h.covered {
				continue
			}
			p, ok := h.pct()
			if len(covered) > reportFoldHosts && ok && p >= 100 && len(h.probs) == 0 {
				clean++
				continue
			}
			row := report.HostRow{Name: h.h.Name, Group: primarySite(h.groups), Uptime: "-", UptimeTone: report.Muted,
				Downtime: fmtSpan(h.down), Problems: strconv.Itoa(len(h.probs)), Longest: "-"}
			if ok {
				row.Uptime, row.UptimeTone = fmtPct(p), pctTone(p, spec.Target)
				row.Under = spec.Target > 0 && p < spec.Target
			}
			if h.longest > 0 {
				row.Longest = fmtSpan(h.longest)
			}
			doc.Hosts = append(doc.Hosts, row)
		}
		if clean > 0 {
			doc.HostsMore = plural(clean, "more host", "more hosts") + " at 100 %, no problems"
		}
	}

	// The sections, with each host's sensors (and their rows on page 1).
	closeUpsLeft, noHistory := reportCloseUpsAll, 0
	for _, h := range hosts {
		sec := report.Section{Name: h.h.Name}
		sub := []string{}
		if g := primarySite(h.groups); g != "" {
			sub = append(sub, g)
		}
		if c, ok := provision.ClassByID(classes[h.h.HostID]); ok {
			sub = append(sub, c.Label)
		}
		if ip := ips[h.h.HostID]; ip != "" {
			sub = append(sub, ip)
		}
		sec.Sub = strings.Join(sub, " - ")
		if h.covered {
			sec.Uptime, sec.UptimeTone = "no data", report.Muted
			if p, ok := h.pct(); ok {
				sec.Uptime, sec.UptimeTone = fmtPct(p), pctTone(p, spec.Target)
			} else if h.item == "" {
				sec.Uptime = "no uptime sensor"
			}
			sec.Days = reportDays(h, from, to, loc, spec.Target)
			if len(h.probs) == 0 {
				sec.NoProblems = "No problems in the period."
			}
			for i, v := range h.probs {
				if i == reportProblemRows {
					sec.ProblemsMore = plural(len(h.probs)-i, "more problem", "more problems")
					break
				}
				sec.Problems = append(sec.Problems, reportProblem(v, run.now, loc, s.mgr.Clock24h()))
			}
		}
		if hc, ok := hostCharts[h.h.HostID]; ok {
			sec.Charts = append(sec.Charts, s.hostChart(ctx, hc, h, items, trigs, from, to, loc, run.now, sec.Uptime)...)
		}
		if !spec.SkipCloseUps && h.covered && h.item != "" && closeUpsLeft > 0 {
			cs, old := s.closeUps(ctx, h, items[h.item], items[rtt[h.h.HostID]], hours[h.item], from, to, loc, run.now, min(reportCloseUps, closeUpsLeft))
			sec.Charts = append(sec.Charts, cs...)
			closeUpsLeft -= len(cs)
			noHistory += old
		}
		for _, sn := range h.sensors {
			charts, row := s.sensorReport(ctx, sn, h, items, trigs, from, to, loc, run.now)
			sec.Charts = append(sec.Charts, charts...)
			doc.Sensors = append(doc.Sensors, row)
		}
		doc.Sections = append(doc.Sections, sec)
	}

	// What the figures mean, and what couldn't be read.
	notes := []string{}
	if len(covered) > 0 {
		notes = append(notes, "Uptime is the share of checks that found a host up, on its availability sensor (its ping, unless another sensor is its master), read hour by hour.")
		if spec.SkipMaintenance {
			if totMaint > 0 {
				notes = append(notes, "Maintenance windows ("+fmtSpan(totMaint)+" in all) are left out of the uptime and the downtime.")
			} else {
				notes = append(notes, "Maintenance windows are left out of the uptime; none ran in the period.")
			}
		} else {
			notes = append(notes, "Maintenance windows count like any other time.")
		}
	}
	if len(hostCharts) > 0 {
		notes = append(notes, "Each host's ping chart shows its response time, the time it was down shaded red (as counted in its uptime).")
	}
	if nSensors > 0 || len(hostCharts) > 0 {
		if to.Sub(from) > reportHistoryDays*24*time.Hour {
			notes = append(notes, "Charts show hourly averages; min and max are the extremes within the hours, 95th the value the hourly averages stayed under 95 % of the time.")
		} else {
			notes = append(notes, "95th: the value the sensor stayed under 95 % of the time.")
		}
		notes = append(notes, "In warning and in error: how long the sensor's own alerts were open.")
	}
	if closeUpsLeft < reportCloseUpsAll {
		notes = append(notes, "A close-up shows an outage minute by minute from Zabbix's raw checks, with the time around it; up to "+strconv.Itoa(reportCloseUps)+" per host, the longest first.")
	}
	if staleHours > 0 {
		notes = append(notes, plural(staleHours, "hour", "hours")+" with a failed check, older than the raw checks Zabbix keeps, count as down in full: Zabbix's hourly figures of an up/down sensor are whole numbers.")
	}
	if noHistory > 0 {
		notes = append(notes, plural(noHistory, "outage has", "outages have")+" no close-up: older than the raw checks Zabbix keeps (Settings, Data retention).")
	}
	for _, sec := range doc.Sections {
		if slices.ContainsFunc(sec.Days, func(d report.Day) bool { return d.Tone == report.Muted }) {
			notes = append(notes, "A grey day in a host's strip had no checks: the host wasn't monitored yet, or none reached Argus.")
			break
		}
	}
	notes = append(notes, "Times are in "+loc.String()+".")
	if upErr != nil {
		notes = append(notes, "Some uptime could not be read from Zabbix: "+zabbix.UserMessage(upErr)+".")
	}
	if incErr != nil {
		notes = append(notes, "The problems could not be read from Zabbix: "+zabbix.UserMessage(incErr)+".")
	}
	if len(spec.Sensors) > reportMaxSensors {
		notes = append(notes, "The first "+strconv.Itoa(reportMaxSensors)+" sensors are charted; the others are left out.")
	}
	doc.Footnote = strings.Join(notes, " ")
	return doc, nil
}

// reportScopeLine is the first line at the top right of page 1: what the report covers.
func reportScopeLine(spec store.ReportSpec, hosts, sensors int) string {
	var parts []string
	if len(spec.Sites) > 0 {
		parts = append(parts, strings.Join(spec.Sites, ", "))
	} else if hosts > 0 {
		parts = append(parts, plural(hosts, "host", "hosts"))
	}
	if sensors > 0 {
		parts = append(parts, plural(sensors, "sensor", "sensors"))
	}
	return strings.Join(parts, "; ")
}

// fmtStamp reads a moment as "Oct 1, 2026 at 07:00" (or 7:00 AM).
func fmtStamp(t time.Time, loc *time.Location, h24 bool) string {
	if h24 {
		return t.In(loc).Format("Jan 2, 2006 at 15:04")
	}
	return t.In(loc).Format("Jan 2, 2006 at 3:04 PM")
}

// reportDays is a host's strip: a cell per day, or per week past two months.
func reportDays(h *reportHost, from, to time.Time, loc *time.Location, target float64) []report.Day {
	var days []string
	for d := startOfDay(from, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		days = append(days, dayKey(d, loc))
	}
	per := 1
	if len(days) > 62 {
		per = 7
	}
	var out []report.Day
	for i := 0; i < len(days); i += per {
		var a upAgg
		for _, d := range days[i:min(i+per, len(days))] {
			a.merge(h.days[d])
		}
		tone := report.Muted
		if p, ok := aggPct(a); ok {
			tone = pctTone(p, target)
		}
		out = append(out, report.Day{Tone: tone})
	}
	return out
}

// reportProblem is a problem's line in its host's table.
func reportProblem(v incidentView, now time.Time, loc *time.Location, h24 bool) report.Problem {
	layout := "Jan 2, 15:04"
	if !h24 {
		layout = "Jan 2, 3:04 PM"
	}
	p := report.Problem{Started: time.Unix(v.Start, 0).In(loc).Format(layout), Name: v.Name, Sensor: v.Sensor, Tone: report.Warn}
	if v.Severity >= 3 {
		p.Tone = report.Err
	}
	if v.End == 0 {
		p.Lasted = "open, " + fmtSpan(now.Sub(time.Unix(v.Start, 0)))
	} else {
		p.Lasted = fmtSpan(time.Duration(v.End-v.Start) * time.Second)
	}
	switch {
	case v.AckBy != "" && v.AckNote != "":
		p.Ack = v.AckBy + ": " + v.AckNote
	case v.AckBy != "":
		p.Ack = v.AckBy
	case v.Note != "":
		p.Ack = "Note: " + v.Note
	}
	return p
}

// closeUps draws a host's outages up close. The hours its availability sensor read down (from the
// trends its uptime came from, without the ones maintenance left out) are grouped into stretches, the
// most downtime first; each is read check by check from Zabbix's raw history, an hour either side,
// and drawn from half its length before it (30 minutes at least, 6 hours at most) to as long after:
// the minutes it was down marked red, its ping's response time around them, and the problems open
// then. old counts the stretches with no raw history left; a host with more outages than n says how
// many weren't drawn under its last close-up.
func (s *Server) closeUps(ctx context.Context, h *reportHost, up, rtt zabbix.Item, hours []trendHour, from, to time.Time, loc *time.Location, now time.Time, n int) (charts []report.Chart, old int) {
	if up.ItemID == "" || n <= 0 {
		return nil, 0
	}
	type stretch struct {
		a, b time.Time
		down time.Duration
	}
	sorted := append([]trendHour(nil), hours...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].at.Before(sorted[j].at) })
	var cands []stretch
	for _, tr := range sorted {
		if tr.num == 0 || tr.avg >= 0.999 || overlapsAny(h.maint, tr.at, tr.at.Add(time.Hour)) {
			continue
		}
		d := time.Duration((1 - math.Max(0, math.Min(1, tr.avg))) * float64(time.Hour))
		if k := len(cands) - 1; k >= 0 && !tr.at.After(cands[k].b.Add(time.Hour)) {
			cands[k].b, cands[k].down = tr.at.Add(time.Hour), cands[k].down+d
			continue
		}
		cands = append(cands, stretch{tr.at, tr.at.Add(time.Hour), d})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].down > cands[j].down })
	more := 0
	if len(cands) > n {
		more, cands = len(cands)-n, cands[:n]
	}
	layout, short := "Jan 2, 15:04", "15:04"
	if !s.mgr.Clock24h() {
		layout, short = "Jan 2, 3:04 PM", "3:04 PM"
	}
	for _, c := range cands {
		hist, err := s.zbx.History(ctx, up.ItemID, atoi(up.ValueType), c.a.Add(-time.Hour).Unix(), c.b.Add(time.Hour).Unix())
		if err != nil || len(hist) == 0 {
			old++
			continue
		}
		var eps []report.Span
		var down time.Duration
		for _, e := range downEpisodes(hist) {
			if overlapsAny(h.maint, e.From, e.To) || !e.To.After(from) || !e.From.Before(to) {
				continue
			}
			eps = append(eps, e)
			down += e.To.Sub(e.From)
		}
		if len(eps) == 0 {
			continue
		}
		start, end := eps[0].From, eps[len(eps)-1].To
		pad := max(30*time.Minute, end.Sub(start)/2)
		pad = min(pad, 6*time.Hour)
		wa, wb := start.Add(-pad), end.Add(pad)
		if wb.After(now) {
			wb = now
		}
		when := start.In(loc).Format(layout) + " to " + end.In(loc).Format(short)
		if start.In(loc).YearDay() != end.In(loc).YearDay() {
			when = start.In(loc).Format(layout) + " to " + end.In(loc).Format(layout)
		}
		ch := report.Chart{Title: "Down " + when, Stats: []report.Stat{{Label: "down", Value: fmtSpan(down)}},
			From: wa, To: wb, Loc: loc, Down: eps, Compact: true}
		if rtt.ItemID != "" {
			if rh, err := s.zbx.History(ctx, rtt.ItemID, atoi(rtt.ValueType), wa.Unix(), wb.Unix()); err == nil {
				var pts []report.Point
				for _, p := range rh {
					if v, err := strconv.ParseFloat(p.Value, 64); err == nil {
						pts = append(pts, report.Point{T: time.Unix(atoi64(p.Clock), 0), V: v})
					}
				}
				if len(pts) > 0 {
					ch.Series = []report.Series{{Points: pts}}
					ch.Label = func(v float64) string {
						if v == 0 {
							return "0"
						}
						return notify.FormatReading(strconv.FormatFloat(v, 'f', -1, 64), rtt.Units)
					}
				}
			}
		}
		if len(ch.Series) == 0 { // no ping: the flag itself, up and down
			if fh, err := s.zbx.History(ctx, up.ItemID, atoi(up.ValueType), wa.Unix(), wb.Unix()); err == nil {
				var pts []report.Point
				for _, p := range fh {
					if v, err := strconv.ParseFloat(p.Value, 64); err == nil {
						pts = append(pts, report.Point{T: time.Unix(atoi64(p.Clock), 0), V: v})
					}
				}
				ch.Kind, ch.Series = report.UpDownChart, []report.Series{{Points: pts}}
			}
		}
		var open []string
		for _, v := range h.probs {
			e := now
			if v.End != 0 {
				e = time.Unix(v.End, 0)
			}
			if time.Unix(v.Start, 0).Before(end) && e.After(start) && !slices.Contains(open, v.Name) {
				open = append(open, v.Name)
			}
		}
		ch.Foot = "no problem was raised for it"
		if len(open) > 0 {
			ch.Foot = "open then: " + strings.Join(open, "; ")
		}
		charts = append(charts, ch)
	}
	if more > 0 && len(charts) > 0 {
		charts[len(charts)-1].Foot += "; " + plural(more, "shorter outage", "shorter outages") + " not drawn up close"
	}
	return charts, old
}

// downEpisodes reads the stretches an up/down sensor's raw checks found it down: from the first
// check that read down to the first that read up again (or a check's interval past the last), with
// gaps of under 10 minutes between them joined.
func downEpisodes(hist []zabbix.HistoryPoint) []report.Span {
	var times []time.Time
	var vals []float64
	for _, p := range hist {
		if v, err := strconv.ParseFloat(p.Value, 64); err == nil {
			times, vals = append(times, time.Unix(atoi64(p.Clock), 0)), append(vals, v)
		}
	}
	step := time.Minute
	if len(times) > 1 {
		gaps := make([]time.Duration, 0, len(times)-1)
		for i := 1; i < len(times); i++ {
			gaps = append(gaps, times[i].Sub(times[i-1]))
		}
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		step = gaps[len(gaps)/2]
	}
	var out []report.Span
	var cur *report.Span
	for i, t := range times {
		if vals[i] < 0.5 {
			if cur == nil {
				cur = &report.Span{From: t}
			}
			cur.To = t.Add(step)
			continue
		}
		if cur != nil {
			cur.To = t
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	var joined []report.Span
	for _, e := range out {
		if k := len(joined) - 1; k >= 0 && e.From.Sub(joined[k].To) < 10*time.Minute {
			joined[k].To = e.To
			continue
		}
		joined = append(joined, e)
	}
	return joined
}

// hostChart is a covered host's availability chart: "Ping" (or its collector's flag), drawn like a
// picked sensor, but its down time agrees with the host's uptime: without the hours a maintenance
// window left out, and the host's own downtime in its figures.
func (s *Server) hostChart(ctx context.Context, sn store.ReportSensor, h *reportHost, items map[string]zabbix.Item, trigs map[string][]zabbix.ItemTrigger, from, to time.Time, loc *time.Location, now time.Time, uptime string) []report.Chart {
	if it, ok := items[h.item]; ok {
		sn.Label = "Ping"
		if it.Key != defaultMasterKey {
			sn.Label = sensorLabel(it.Key, it.Name)
		}
	}
	charts, _ := s.sensorReport(ctx, sn, h, items, trigs, from, to, loc, now)
	for i := range charts {
		c := &charts[i]
		down := c.Down[:0:0]
		for _, d := range c.Down {
			if !overlapsAny(h.maint, d.From, d.To) {
				down = append(down, d)
			}
		}
		c.Down = down
		if c.Kind == report.UpDownChart {
			c.Stats = []report.Stat{{Label: "uptime", Value: uptime}, {Label: "down", Value: fmtSpan(h.down)}}
		} else if c.Foot != "" {
			if i := strings.Index(c.Foot, "; down "); i >= 0 {
				c.Foot = c.Foot[:i]
			}
			if h.down > 0 {
				c.Foot += "; down " + fmtSpan(h.down) + " (shaded)"
			}
		}
	}
	return charts
}

// sensorReport is a picked sensor's charts and its row on page 1. A group's channels share a chart
// when they share units (one chart per kind of units otherwise); its up/down channels shade the time
// they read down.
func (s *Server) sensorReport(ctx context.Context, sn store.ReportSensor, h *reportHost, items map[string]zabbix.Item, trigs map[string][]zabbix.ItemTrigger, from, to time.Time, loc *time.Location, now time.Time) ([]report.Chart, report.SensorRow) {
	label := strings.TrimSpace(sn.Label)
	row := report.SensorRow{Name: label, Host: h.h.Name, Min: "-", Avg: "-", Max: "-", P95: "-", InWarn: "0", InErr: "0"}
	type channel struct {
		it   zabbix.Item
		name string
		ss   series
	}
	var numeric, updown []channel
	for _, id := range sn.Items {
		it, ok := items[id]
		if !ok || !numericValueType(it.ValueType) {
			continue
		}
		if label == "" {
			label = sensorLabel(it.Key, it.Name)
			row.Name = label
		}
		_, _, _, ch, _ := classifyItem(it.Key, it.Name)
		if ch == "" {
			ch = sensorLabel(it.Key, it.Name)
		}
		ss, ok := s.readSeries(ctx, it, from, to)
		if !ok {
			continue
		}
		c := channel{it: it, name: ch, ss: ss}
		if isUpDownKey(it.Key) {
			updown = append(updown, c)
		} else {
			numeric = append(numeric, c)
		}
	}
	if row.Name == "" {
		row.Name = "Sensor"
	}

	// Time in warning and in error: its own alerts while they were open.
	want := map[string]bool{}
	for _, id := range sn.Items {
		want[id] = true
	}
	var inWarn, inErr time.Duration
	nProbs := 0
	for _, v := range h.probs {
		if !want[v.ItemID] {
			continue
		}
		nProbs++
		e := now
		if v.End != 0 {
			e = time.Unix(v.End, 0)
		}
		d := clipped(time.Unix(v.Start, 0), e, from, to)
		if v.Severity >= 3 {
			inErr += d
		} else {
			inWarn += d
		}
	}
	row.InWarn, row.InErr = fmtSpan(inWarn), fmtSpan(inErr)
	if inWarn > 0 {
		row.WarnTone = report.Warn
	}
	if inErr > 0 {
		row.ErrTone = report.Err
	}
	foot := "in warning " + row.InWarn + ", in error " + row.InErr

	if len(numeric) == 0 && len(updown) == 0 {
		return []report.Chart{{Title: row.Name, Kind: report.UpDownChart, From: from, To: to, Loc: loc, Foot: "no values in the period"}}, row
	}
	var down []report.Span
	var downFor time.Duration
	for _, c := range updown {
		sp, d := downOf(c.ss)
		down = append(down, sp...)
		if d > downFor {
			downFor = d
		}
	}
	if len(numeric) == 0 {
		c := updown[0]
		up := 100.0
		if downFor > 0 {
			up = math.Min(100*c.ss.avg, 99.999)
		}
		row.Avg = fmtPct(up)
		stats := []report.Stat{{Label: "uptime", Value: row.Avg}, {Label: "down", Value: fmtSpan(downFor)}}
		if nProbs > 0 {
			stats[1].Value += " in " + plural(nProbs, "problem", "problems")
		}
		return []report.Chart{{Title: row.Name, Kind: report.UpDownChart, Stats: stats, From: from, To: to, Loc: loc,
			Series: []report.Series{{Points: c.ss.pts}}, Down: down}}, row
	}

	// A group leads with the channel the host page headlines (its response time, a disk's used %, a
	// port's traffic in, the download): the figures on page 1 and its first chart are that channel's.
	sort.SliceStable(numeric, func(i, j int) bool { return leadRank(numeric[i].name) < leadRank(numeric[j].name) })
	first := numeric[0]
	fmtV := func(v float64) string {
		return notify.FormatReading(strconv.FormatFloat(v, 'f', -1, 64), first.it.Units)
	}
	row.Min, row.Avg, row.Max, row.P95 = fmtV(first.ss.min), fmtV(first.ss.avg), fmtV(first.ss.max), fmtV(first.ss.p95)
	if len(numeric) > 1 {
		row.Name += ", " + first.name
	}

	// One chart per kind of units, in the channels' order.
	var unitOrder []string
	byUnit := map[string][]channel{}
	for _, c := range numeric {
		if _, ok := byUnit[c.it.Units]; !ok {
			unitOrder = append(unitOrder, c.it.Units)
		}
		byUnit[c.it.Units] = append(byUnit[c.it.Units], c)
	}
	var charts []report.Chart
	for i, u := range unitOrder {
		chs := byUnit[u]
		units := u
		lbl := func(v float64) string {
			if v == 0 && units == "s" {
				return "0" // not "0 s" under "1 ms"
			}
			return notify.FormatReading(strconv.FormatFloat(v, 'f', -1, 64), units)
		}
		c := report.Chart{Title: label, From: from, To: to, Loc: loc, Label: lbl, Base1024: units == "B" || units == "Bps", Down: down}
		if len(unitOrder) > 1 {
			names := make([]string, 0, len(chs))
			for _, ch := range chs {
				names = append(names, ch.name)
			}
			c.Title = label + ": " + strings.Join(names, ", ")
		}
		lead := chs[0]
		if len(chs) > 1 {
			c.Stats = append(c.Stats, report.Stat{Label: lead.name})
		}
		c.Stats = append(c.Stats, report.Stat{Label: "min", Value: lbl(lead.ss.min)}, report.Stat{Label: "avg", Value: lbl(lead.ss.avg)},
			report.Stat{Label: "max", Value: lbl(lead.ss.max)}, report.Stat{Label: "95th", Value: lbl(lead.ss.p95)})
		for _, ch := range chs {
			c.Series = append(c.Series, report.Series{Name: ch.name, Points: ch.ss.pts})
		}
		if thr := itemThresholdsFrom(trigs[lead.it.ItemID], lead.it.Key); thr != nil {
			c.Below = thr.Below
			if thr.Warn != nil {
				c.Thresholds = append(c.Thresholds, report.Threshold{V: *thr.Warn, Tone: report.Warn})
			}
			if thr.High != nil {
				c.Thresholds = append(c.Thresholds, report.Threshold{V: *thr.High, Tone: report.Err})
			}
		}
		if i == 0 {
			c.Foot = foot
			if len(down) > 0 {
				c.Foot += "; down " + fmtSpan(downFor) + " (shaded)"
			}
		}
		charts = append(charts, c)
	}
	return charts, row
}
