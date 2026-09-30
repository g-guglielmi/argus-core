// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Uptime of an up/down sensor (ping, a TCP/HTTP(S) service check, a collector's reachable flag, a
// DNS name resolving): it reads 1 while up and 0 while down, so its average over a period is the
// share of checks that found it up. 24 h comes from the raw history; 7 and 30 days are calendar days
// in the Argus timezone (today and the days before it), from Zabbix's hourly trends weighted by their
// sample counts, plus the hour the trends don't cover yet. A status page reads many hosts at once, so
// complete days are kept in uptime_days and only today is read from Zabbix each time.

const (
	uptimeWindowDays = 30              // days shown and summed for "30 days"
	uptimeChecks     = 60              // recent checks in the strip
	uptimeCacheTTL   = time.Minute     // one sensor's view, for the host page's polling
	uptimeBatchTTL   = 5 * time.Minute // many hosts' days, for status pages
	uptimeTrendLag   = 2 * time.Hour   // a day counts as complete this long after it ends
	uptimeBatchItems = 20              // items per trend.get
	uptimeKeepDays   = 120             // stored days kept
)

// isUpDownKey reports whether a sensor reads 1 up / 0 down, so its average over time is its uptime.
func isUpDownKey(key string) bool {
	return isReachabilityKey(key) || (reasonKeyFor(key) != "" && !isCountKey(key))
}

// upAgg sums checks: up is the number found up (a fraction per hourly trend, weighted), num all of them.
type upAgg struct {
	up  float64
	num int64
}

func (a *upAgg) add(avg float64, num int64) {
	if num <= 0 {
		return
	}
	a.up += avg * float64(num)
	a.num += num
}

func (a *upAgg) merge(b upAgg) { a.up += b.up; a.num += b.num }

// pct is the uptime in percent (3 decimals), nil without a single check.
func (a upAgg) pct() *float64 {
	if a.num == 0 {
		return nil
	}
	v := math.Round(100*a.up/float64(a.num)*1000) / 1000
	if v > 100 {
		v = 100
	}
	return &v
}

func dayKey(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

func startOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// windowDays is the last n local calendar days ending with today's, oldest first.
func windowDays(now time.Time, loc *time.Location, n int) []string {
	today := startOfDay(now, loc)
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}

// sumDays adds up the last n of the window's days.
func sumDays(days map[string]upAgg, window []string, n int) upAgg {
	var a upAgg
	if n > len(window) {
		n = len(window)
	}
	for _, d := range window[len(window)-n:] {
		a.merge(days[d])
	}
	return a
}

type uptimeDayView struct {
	Day string   `json:"day"`
	Pct *float64 `json:"pct"` // nil: no data that day
}

// availabilityView is one up/down sensor's uptime: the three figures, its last checks (oldest
// first, [unix, 1|0]) and one bar per day of the window.
type availabilityView struct {
	ItemID   string          `json:"item_id"`
	Label    string          `json:"label,omitempty"`
	Uptime24 *float64        `json:"uptime_24h"`
	Uptime7  *float64        `json:"uptime_7d"`
	Uptime30 *float64        `json:"uptime_30d"`
	Checks   [][2]int64      `json:"checks"`
	Days     []uptimeDayView `json:"days"`
}

// buildAvailability computes a sensor's view from its trend rows (the window) and its last 24 h of
// history. History newer than the last trend hour fills the hour the trends don't cover yet.
func buildAvailability(itemID string, trends []zabbix.TrendRow, hist []zabbix.HistoryPoint, now time.Time, loc *time.Location) availabilityView {
	window := windowDays(now, loc, uptimeWindowDays)
	days := map[string]upAgg{}
	var lastTrend int64
	for _, r := range trends {
		clock := atoi64(r.Clock)
		avg, err := strconv.ParseFloat(r.ValueAvg, 64)
		if err != nil {
			continue
		}
		k := dayKey(time.Unix(clock, 0), loc)
		a := days[k]
		a.add(avg, atoi64(r.Num))
		days[k] = a
		if clock > lastTrend {
			lastTrend = clock
		}
	}
	var day upAgg
	checks := make([][2]int64, 0, len(hist))
	for _, h := range hist {
		v, err := strconv.ParseFloat(h.Value, 64)
		if err != nil {
			continue
		}
		up := int64(0)
		if v >= 0.5 {
			up = 1
		}
		clock := atoi64(h.Clock)
		day.add(float64(up), 1)
		checks = append(checks, [2]int64{clock, up})
		if lastTrend == 0 || clock >= lastTrend+3600 {
			k := dayKey(time.Unix(clock, 0), loc)
			a := days[k]
			a.add(float64(up), 1)
			days[k] = a
		}
	}
	if len(checks) > uptimeChecks {
		checks = checks[len(checks)-uptimeChecks:]
	}
	v := availabilityView{ItemID: itemID, Uptime24: day.pct(), Checks: checks,
		Uptime7: sumDays(days, window, 7).pct(), Uptime30: sumDays(days, window, uptimeWindowDays).pct()}
	for _, d := range window {
		v.Days = append(v.Days, uptimeDayView{Day: d, Pct: days[d].pct()})
	}
	return v
}

var availCache = struct {
	mu sync.Mutex
	m  map[string]availEntry
}{m: map[string]availEntry{}}

type availEntry struct {
	at time.Time
	v  availabilityView
}

// availability is one sensor's view, cached a minute.
func (s *Server) availability(ctx context.Context, it zabbix.Item) (availabilityView, error) {
	now := time.Now()
	availCache.mu.Lock()
	if e, ok := availCache.m[it.ItemID]; ok && now.Sub(e.at) < uptimeCacheTTL {
		availCache.mu.Unlock()
		return e.v, nil
	}
	availCache.mu.Unlock()

	loc := s.mgr.Location()
	from := startOfDay(now, loc).AddDate(0, 0, -(uptimeWindowDays - 1))
	trends, err := s.zbx.TrendRows(ctx, []string{it.ItemID}, from.Unix(), now.Unix())
	if err != nil {
		return availabilityView{}, err
	}
	hist, err := s.zbx.History(ctx, it.ItemID, atoi(it.ValueType), now.Add(-24*time.Hour).Unix(), now.Unix())
	if err != nil {
		return availabilityView{}, err
	}
	v := buildAvailability(it.ItemID, trends, hist, now, loc)
	v.Label = sensorLabel(it.Key, it.Name)

	availCache.mu.Lock()
	for id, e := range availCache.m {
		if now.Sub(e.at) >= uptimeCacheTTL {
			delete(availCache.m, id)
		}
	}
	availCache.m[it.ItemID] = availEntry{at: now, v: v}
	availCache.mu.Unlock()
	return v, nil
}

// handleItemAvailability serves GET /api/items/{id}/availability for an up/down sensor.
func (s *Server) handleItemAvailability(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	it, err := s.zbx.Item(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	if !isUpDownKey(it.Key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uptime applies to up/down sensors only"})
		return
	}
	v, err := s.availability(ctx, *it)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hostUptimeItem picks the sensor a host's uptime is measured on: its master sensor when that one is
// an up/down sensor, else its ping, else a collector's reachable flag. ok is false when it has none.
func hostUptimeItem(items []zabbix.Item, override string) (zabbix.Item, bool) {
	if override != "" {
		for _, it := range items {
			if it.ItemID == override && isUpDownKey(it.Key) {
				return it, true
			}
		}
	}
	for _, it := range items {
		if it.Key == defaultMasterKey && it.Status == "0" {
			return it, true
		}
	}
	for _, k := range collectorMasterKeys {
		for _, it := range items {
			if it.Key == k && it.Status == "0" {
				return it, true
			}
		}
	}
	return zabbix.Item{}, false
}

// handleHostAvailability serves GET /api/hosts/{id}/availability: the host's uptime, measured on
// hostUptimeItem. An empty item_id means the host has no up/down sensor.
func (s *Server) handleHostAvailability(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	hostID := r.PathValue("id")
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	override, _, _ := s.st.HostMaster(ctx, hostID)
	it, ok := hostUptimeItem(items, override)
	if !ok {
		writeJSON(w, http.StatusOK, availabilityView{Checks: [][2]int64{}, Days: []uptimeDayView{}})
		return
	}
	v, err := s.availability(ctx, it)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hostUptime is one host's 7 and 30 day uptime, for status pages.
type hostUptime struct {
	D7, D30 *float64
}

var uptimeDaysCache = struct {
	mu        sync.Mutex
	m         map[string]uptimeDaysEntry
	lastPrune time.Time
}{m: map[string]uptimeDaysEntry{}}

type uptimeDaysEntry struct {
	at   time.Time
	days map[string]upAgg
}

// hostUptimes returns the 7 and 30 day uptime of the given hosts (those without an up/down sensor are
// left out).
func (s *Server) hostUptimes(ctx context.Context, hostIDs []string) map[string]hostUptime {
	out := map[string]hostUptime{}
	if len(hostIDs) == 0 {
		return out
	}
	want := map[string]bool{}
	for _, h := range hostIDs {
		want[h] = true
	}
	// One uptime sensor per host, the same pick as the host page (hostUptimeItem).
	byHost := map[string][]zabbix.Item{}
	if items, err := s.zbx.ItemsByKeys(ctx, append([]string{defaultMasterKey}, collectorMasterKeys...)); err == nil {
		for _, it := range items {
			if want[it.HostID] {
				byHost[it.HostID] = append(byHost[it.HostID], it)
			}
		}
	}
	overrides, _ := s.st.HostMasters(ctx)
	var custom []string
	for h, id := range overrides {
		if want[h] && id != "" {
			custom = append(custom, id)
		}
	}
	if len(custom) > 0 {
		if items, err := s.zbx.ItemsByIDs(ctx, custom); err == nil {
			for _, it := range items {
				it.Status = "0"
				byHost[it.HostID] = append(byHost[it.HostID], it)
			}
		}
	}
	itemOf := map[string]string{}
	var ids []string
	for h, items := range byHost {
		if it, ok := hostUptimeItem(items, overrides[h]); ok {
			itemOf[h] = it.ItemID
			ids = append(ids, it.ItemID)
		}
	}
	now := time.Now()
	loc := s.mgr.Location()
	window := windowDays(now, loc, uptimeWindowDays)
	days := s.uptimeDaysFor(ctx, ids, now, loc)
	for h, id := range itemOf {
		d := days[id]
		out[h] = hostUptime{D7: sumDays(d, window, 7).pct(), D30: sumDays(d, window, uptimeWindowDays).pct()}
	}
	return out
}

// uptimeDaysFor returns each item's checks per day of the window. Complete days come from
// uptime_days (read from Zabbix once, then stored); the rest are read from Zabbix's trends.
func (s *Server) uptimeDaysFor(ctx context.Context, itemIDs []string, now time.Time, loc *time.Location) map[string]map[string]upAgg {
	out := map[string]map[string]upAgg{}
	var todo []string
	uptimeDaysCache.mu.Lock()
	for _, id := range itemIDs {
		if e, ok := uptimeDaysCache.m[id]; ok && now.Sub(e.at) < uptimeBatchTTL {
			out[id] = e.days
		} else {
			todo = append(todo, id)
		}
	}
	prune := now.Sub(uptimeDaysCache.lastPrune) > 24*time.Hour
	if prune {
		uptimeDaysCache.lastPrune = now
	}
	uptimeDaysCache.mu.Unlock()
	if prune {
		_ = s.st.PruneUptimeDays(ctx, startOfDay(now, loc).AddDate(0, 0, -uptimeKeepDays).Format("2006-01-02"))
	}
	if len(todo) == 0 {
		return out
	}

	window := windowDays(now, loc, uptimeWindowDays)
	complete := func(day string) bool {
		t, err := time.ParseInLocation("2006-01-02", day, loc)
		return err == nil && !now.Before(t.AddDate(0, 0, 1).Add(uptimeTrendLag))
	}
	stored := map[string]map[string]upAgg{}
	if rows, err := s.st.UptimeDays(ctx, todo, window[0]); err == nil {
		for _, r := range rows {
			if stored[r.ItemID] == nil {
				stored[r.ItemID] = map[string]upAgg{}
			}
			stored[r.ItemID][r.Day] = upAgg{up: r.Up, num: r.Num}
		}
	}
	// Items grouped by the first complete day they are missing, so a new host doesn't make every
	// other one read its whole window again.
	missingFrom := map[string][]string{}
	firstLive := ""
	for _, d := range window {
		if !complete(d) {
			firstLive = d
			break
		}
	}
	for _, id := range todo {
		for _, d := range window {
			if !complete(d) {
				break
			}
			if _, ok := stored[id][d]; !ok {
				missingFrom[d] = append(missingFrom[d], id)
				break
			}
		}
	}
	fetch := func(ids []string, fromDay string, toTime time.Time) map[string]map[string]upAgg {
		got := map[string]map[string]upAgg{}
		from, err := time.ParseInLocation("2006-01-02", fromDay, loc)
		if err != nil {
			return got
		}
		for start := 0; start < len(ids); start += uptimeBatchItems {
			chunk := ids[start:min(start+uptimeBatchItems, len(ids))]
			rows, err := s.zbx.TrendRows(ctx, chunk, from.Unix(), toTime.Unix()-1)
			if err != nil {
				continue
			}
			for _, r := range rows {
				avg, err := strconv.ParseFloat(r.ValueAvg, 64)
				if err != nil {
					continue
				}
				k := dayKey(time.Unix(atoi64(r.Clock), 0), loc)
				if got[r.ItemID] == nil {
					got[r.ItemID] = map[string]upAgg{}
				}
				a := got[r.ItemID][k]
				a.add(avg, atoi64(r.Num))
				got[r.ItemID][k] = a
			}
		}
		return got
	}
	liveStart := now
	if firstLive != "" {
		if t, err := time.ParseInLocation("2006-01-02", firstLive, loc); err == nil {
			liveStart = t
		}
	}
	var puts []store.UptimeDay
	for fromDay, ids := range missingFrom {
		got := fetch(ids, fromDay, liveStart)
		for _, id := range ids {
			if stored[id] == nil {
				stored[id] = map[string]upAgg{}
			}
			for _, d := range window {
				if d < fromDay || !complete(d) {
					continue
				}
				if _, ok := stored[id][d]; ok {
					continue
				}
				a := got[id][d]
				stored[id][d] = a
				puts = append(puts, store.UptimeDay{ItemID: id, Day: d, Up: a.up, Num: a.num})
			}
		}
	}
	_ = s.st.PutUptimeDays(ctx, puts)
	live := map[string]map[string]upAgg{}
	if firstLive != "" {
		live = fetch(todo, firstLive, now.Add(time.Second))
	}
	uptimeDaysCache.mu.Lock()
	for _, id := range todo {
		d := map[string]upAgg{}
		for k, a := range stored[id] {
			d[k] = a
		}
		for k, a := range live[id] {
			d[k] = a
		}
		out[id] = d
		uptimeDaysCache.m[id] = uptimeDaysEntry{at: now, days: d}
	}
	for id, e := range uptimeDaysCache.m {
		if now.Sub(e.at) >= uptimeBatchTTL {
			delete(uptimeDaysCache.m, id)
		}
	}
	uptimeDaysCache.mu.Unlock()
	return out
}

// statusUptime is a status page's availability section: the average 30-day uptime of its hosts and
// the ones under 100%, worst first.
type statusUptime struct {
	Overall30 *float64           `json:"overall_30d"`
	Hosts     int                `json:"hosts"` // hosts with an uptime figure
	Below     []statusUptimeHost `json:"below"`
}

type statusUptimeHost struct {
	Host string   `json:"host"`
	Site string   `json:"site"`
	D7   *float64 `json:"uptime_7d"`
	D30  *float64 `json:"uptime_30d"`
}

const statusUptimeBelowMax = 10

// statusUptimeFor builds the availability section for the hosts on a page.
func statusUptimeFor(ups map[string]hostUptime, siteOf, nameOf map[string]string) statusUptime {
	v := statusUptime{Below: []statusUptimeHost{}}
	var sum float64
	for h, u := range ups {
		if u.D30 == nil {
			continue
		}
		v.Hosts++
		sum += *u.D30
		if *u.D30 < 100 {
			v.Below = append(v.Below, statusUptimeHost{Host: nameOf[h], Site: siteOf[h], D7: u.D7, D30: u.D30})
		}
	}
	if v.Hosts > 0 {
		o := math.Round(sum/float64(v.Hosts)*1000) / 1000
		v.Overall30 = &o
	}
	sort.SliceStable(v.Below, func(i, j int) bool {
		if *v.Below[i].D30 != *v.Below[j].D30 {
			return *v.Below[i].D30 < *v.Below[j].D30
		}
		return v.Below[i].Host < v.Below[j].Host
	})
	if len(v.Below) > statusUptimeBelowMax {
		v.Below = v.Below[:statusUptimeBelowMax]
	}
	return v
}
