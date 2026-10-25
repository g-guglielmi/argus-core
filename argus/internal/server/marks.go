// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Chart markers: what happened to a sensor's host in the range a chart shows, so "why did it jump at
// 03:00?" is answered on the chart (DESIGN section 7l). Everything comes from what Argus already
// keeps: the change log (the sensor's own entries and the host's, never another sensor's), the host's
// journal, the maintenance windows that cover it, and its restarts, read from its uptime sensor.

// chartMark is one marker: a moment, or a stretch (a maintenance window) with End.
type chartMark struct {
	T      int64  `json:"t"`
	End    int64  `json:"end,omitempty"`
	Kind   string `json:"kind"`          // change | ack | note | journal | maint | restart
	Sub    string `json:"sub,omitempty"` // a journal entry's kind: info | warning | problem
	What   string `json:"what"`
	Who    string `json:"who,omitempty"`
	Why    string `json:"why,omitempty"`    // the reason given, an acknowledgement's note
	Approx bool   `json:"approx,omitempty"` // a restart read from hourly trends: within that hour
}

const (
	marksMaxItems = 12  // a group chart's channels
	marksMax      = 300 // newest kept when a long range has more
)

// restartKeys are the uptime sensors a restart is read from (the counter starts over); zabbix[uptime]
// on a probe's host is its Zabbix proxy's.
var restartKeys = []string{"system.uptime", "system.uptime[snmp]", "system.uptime[ssh]", "unifi.uptime", "zabbix[uptime]"}

// snmpUptimeWrap is where SNMP's uptime (hundredths of a second in 32 bits) wraps back to 0, after
// 497 days: a fall from there is the counter, not a restart.
const snmpUptimeWrap = 42949672

// GET /api/marks?items=<id,id>&range=<2h|2d|7d|1M|3M|6M|1Y> (anyone who sees the sensors).
func (s *Server) handleMarks(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	rng, ok := timeRanges[r.URL.Query().Get("range")]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid range"})
		return
	}
	var ids []string
	for _, id := range strings.Split(r.URL.Query().Get("items"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > marksMaxItems {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick between 1 and " + strconv.Itoa(marksMaxItems) + " sensors"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ids, err := s.scopedItemIDs(ctx, scopeFrom(r), ids)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	items, err := s.zbx.ItemsByIDs(ctx, ids)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	now := time.Now().Unix()
	from := now - int64(rng.dur.Seconds())
	marks, err := s.chartMarks(ctx, items, from, now, rng.trend)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marks": marks})
}

// chartMarks gathers the markers of these sensors' hosts in [from, to], oldest first. Each source
// that fails is left out rather than failing the rest: the markers only add to a chart.
func (s *Server) chartMarks(ctx context.Context, items map[string]zabbix.Item, from, to int64, trend bool) ([]chartMark, error) {
	itemSet := map[string]bool{}
	hostSet := map[string]bool{}
	var hosts []string
	for id, it := range items {
		itemSet[id] = true
		if !hostSet[it.HostID] {
			hostSet[it.HostID] = true
			hosts = append(hosts, it.HostID)
		}
	}
	sort.Strings(hosts)
	marks := []chartMark{}

	// The change log: the sensors' own entries and their hosts' (an entry from before the log kept
	// its sensors is matched by the name it gave them).
	if changes, err := s.st.ListChanges(ctx, store.ChangeQuery{From: from, HostIDs: hosts, Limit: 2000}); err == nil {
		names := s.sensorNames(ctx, itemSet)
		for _, c := range changes {
			if m, ok := changeMark(c, itemSet, names); ok && c.At <= to {
				marks = append(marks, m)
			}
		}
	}

	// The journal.
	for _, h := range hosts {
		entries, err := s.st.HostJournal(ctx, h)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.CreatedAt >= from && e.CreatedAt <= to {
				marks = append(marks, chartMark{T: e.CreatedAt, Kind: "journal", Sub: e.Kind, What: e.Text, Who: e.ByName})
			}
		}
	}

	// Maintenance windows covering the hosts.
	if windows, err := s.st.MaintenanceWindows(ctx); err == nil && len(windows) > 0 {
		idx, _ := s.hostIndex(ctx)
		loc := s.location()
		for _, w := range windows {
			if !w.Enabled {
				continue
			}
			covers := false
			for _, h := range hosts {
				if windowCovers(w, h, idx[h].Groups) {
					covers = true
					break
				}
			}
			if !covers {
				continue
			}
			for _, sp := range occurrencesBetween(w, time.Unix(from, 0), time.Unix(to, 0), loc) {
				marks = append(marks, chartMark{T: sp.From.Unix(), End: sp.To.Unix(), Kind: "maint", What: "Maintenance: " + w.Name, Who: describeWindow(w)})
			}
		}
	}

	// Restarts, from the hosts' uptime sensors.
	if ups, err := s.zbx.HostItemsByKeys(ctx, hosts, restartKeys); err == nil {
		for _, it := range ups {
			what := "Restarted"
			if it.Key == "zabbix[uptime]" {
				what = "Zabbix restarted"
			}
			if trend {
				rows, err := s.zbx.Trends(ctx, it.ItemID, from-3600, to)
				if err != nil {
					continue
				}
				for _, t := range restartsFromTrends(rows) {
					if t >= from {
						marks = append(marks, chartMark{T: t, Kind: "restart", What: what, Approx: true})
					}
				}
				continue
			}
			vt, _ := strconv.Atoi(it.ValueType)
			pts, err := s.zbx.History(ctx, it.ItemID, vt, from, to)
			if err != nil {
				continue
			}
			for _, t := range restartsFromHistory(pts) {
				marks = append(marks, chartMark{T: t, Kind: "restart", What: what})
			}
		}
	}

	sort.SliceStable(marks, func(i, j int) bool { return marks[i].T < marks[j].T })
	if len(marks) > marksMax {
		marks = marks[len(marks)-marksMax:]
	}
	return marks, nil
}

// sensorNames is "host · sensor" for each item, as the change log names a sensor.
func (s *Server) sensorNames(ctx context.Context, items map[string]bool) []string {
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, row := range rows {
		if !items[row.ItemID] {
			continue
		}
		label := row.Label
		if label == "" {
			label = row.Name
		}
		out = append(out, row.HostName+" · "+label)
	}
	return out
}

// changeMark reads a change-log entry as a marker on a chart of these items, or false when it is about
// another sensor of the host, or is a journal entry (the journal has its own markers).
func changeMark(c store.Change, items map[string]bool, names []string) (chartMark, bool) {
	if strings.HasSuffix(c.Action, "journal entry") {
		return chartMark{}, false
	}
	if c.ItemsKnown {
		if len(c.ItemIDs) > 0 {
			mine := false
			for _, id := range c.ItemIDs {
				if items[id] {
					mine = true
					break
				}
			}
			if !mine {
				return chartMark{}, false
			}
		}
	} else if strings.Contains(c.Object, " · ") {
		mine := false
		for _, n := range names {
			if strings.Contains(c.Object, n) {
				mine = true
				break
			}
		}
		if !mine {
			return chartMark{}, false
		}
	}
	m := chartMark{T: c.At, Kind: "change", Who: c.Actor, Why: c.Reason}
	switch {
	case strings.HasPrefix(c.Action, "Acknowledged"):
		m.Kind = "ack"
	case strings.HasPrefix(c.Action, "Left a note"):
		m.Kind = "note"
	}
	m.What = c.Action
	var parts []string
	if c.Detail != "" {
		parts = append(parts, c.Detail)
	}
	for i, d := range c.Diff {
		if i == 3 {
			parts = append(parts, "and "+strconv.Itoa(len(c.Diff)-3)+" more")
			break
		}
		switch {
		case d.Old == "":
			parts = append(parts, d.Field+" "+d.New)
		case d.New == "":
			parts = append(parts, d.Field+" "+d.Old+" removed")
		default:
			parts = append(parts, d.Field+" "+d.Old+" → "+d.New)
		}
	}
	if len(parts) > 0 {
		m.What += ": " + strings.Join(parts, "; ")
	}
	return m, true
}

// restartsFromHistory finds where an uptime counter started over in raw readings: the boot is the
// reading's time less the uptime it read, never before the reading taken before it.
func restartsFromHistory(pts []zabbix.HistoryPoint) []int64 {
	sort.SliceStable(pts, func(i, j int) bool { return atoi64(pts[i].Clock) < atoi64(pts[j].Clock) })
	var out []int64
	var prevV float64
	var prevT int64
	have := false
	for _, p := range pts {
		v, err := strconv.ParseFloat(p.Value, 64)
		if err != nil {
			continue
		}
		t := atoi64(p.Clock)
		if have && v < prevV-60 && prevV < snmpUptimeWrap-86400 {
			boot := t - int64(v)
			if boot < prevT {
				boot = prevT
			}
			if boot > t {
				boot = t
			}
			out = append(out, boot)
		}
		prevV, prevT, have = v, t, true
	}
	return out
}

// restartsFromTrends finds the hours an uptime counter started over in, from hourly min and max: an
// hour whose lowest reading is far below its highest (the restart was inside it) or below the lowest
// of the hour before (it was at the turn of the hour). The time is a guess within that hour.
func restartsFromTrends(rows []zabbix.TrendPoint) []int64 {
	sort.SliceStable(rows, func(i, j int) bool { return atoi64(rows[i].Clock) < atoi64(rows[j].Clock) })
	var out []int64
	var prevMin float64
	have := false
	for _, r := range rows {
		mn, err1 := strconv.ParseFloat(r.ValueMin, 64)
		mx, err2 := strconv.ParseFloat(r.ValueMax, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		clock := atoi64(r.Clock)
		inside := mx-mn > 3600+300 && mx < snmpUptimeWrap-86400
		atTurn := have && mn < prevMin-300 && prevMin < snmpUptimeWrap-86400
		if inside || atTurn {
			t := clock + 1800 - int64(mn)
			if t < clock {
				t = clock
			}
			if t > clock+3599 {
				t = clock + 3599
			}
			out = append(out, t)
		}
		prevMin, have = mn, true
	}
	return out
}
