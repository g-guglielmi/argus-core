// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"argus/internal/report"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestReportPeriod(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Rome")
	now := time.Date(2026, 10, 1, 7, 0, 0, 0, loc)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, loc) }
	for _, c := range []struct {
		spec     store.ReportSpec
		from, to time.Time
		label    string
	}{
		{store.ReportSpec{Period: "7d"}, day(2026, 9, 24), day(2026, 10, 1), "24 to 30 September 2026"},
		{store.ReportSpec{Period: "30d"}, day(2026, 9, 1), day(2026, 10, 1), "1 to 30 September 2026"},
		{store.ReportSpec{Period: "last_month"}, day(2026, 9, 1), day(2026, 10, 1), "1 to 30 September 2026"},
		{store.ReportSpec{Period: "this_month"}, day(2026, 10, 1), now, "1 October 2026"},
		{store.ReportSpec{Period: "custom", From: "2025-12-28", To: "2026-01-03"}, day(2025, 12, 28), day(2026, 1, 4), "28 December 2025 to 3 January 2026"},
		{store.ReportSpec{Period: "custom", From: "2026-09-28", To: "2026-10-20"}, day(2026, 9, 28), now, "28 September to 1 October 2026"},
	} {
		from, to, label, err := reportPeriod(c.spec, now, loc)
		if err != nil || !from.Equal(c.from) || !to.Equal(c.to) || label != c.label {
			t.Errorf("%+v: got %v .. %v %q (%v), want %v .. %v %q", c.spec, from, to, label, err, c.from, c.to, c.label)
		}
	}
	for _, bad := range []store.ReportSpec{
		{Period: "custom", From: "2026-09-10", To: "2026-09-01"},
		{Period: "custom", From: "2026-11-01", To: "2026-11-30"},
		{Period: "custom", From: "2024-01-01", To: "2026-01-01"},
		{Period: "custom"},
		{Period: "fortnight"},
	} {
		if _, _, _, err := reportPeriod(bad, now, loc); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}

func TestReportFormats(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0", 40 * time.Second: "40 s", 9 * time.Minute: "9 min", 95 * time.Minute: "1 h 35 min",
		2 * time.Hour: "2 h", 76 * time.Hour: "3 d 4 h", 48 * time.Hour: "2 d"} {
		if got := fmtSpan(d); got != want {
			t.Errorf("fmtSpan(%v) = %q, want %q", d, got, want)
		}
	}
	for p, want := range map[float64]string{100: "100 %", 99.999: "99.99 %", 99.81: "99.81 %", 97.5: "97.5 %", 0: "0 %"} {
		if got := fmtPct(p); got != want {
			t.Errorf("fmtPct(%v) = %q, want %q", p, got, want)
		}
	}
	if pctTone(100, 99.9) != report.Good || pctTone(99.95, 99.9) != report.Warn || pctTone(99.8, 99.9) != report.Err || pctTone(98.5, 0) != report.Err {
		t.Error("pctTone: green at 100, amber at or over the target, red under it (99 without one)")
	}
}

func TestReportNext(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Rome")
	at := func(m time.Month, d, h, min int) time.Time { return time.Date(2026, m, d, h, min, 0, 0, loc) }
	for _, c := range []struct {
		sc       store.ReportSchedule
		after    time.Time
		want     time.Time
		describe string
	}{
		{store.ReportSchedule{Every: "day", Minute: 7 * 60}, at(10, 12, 6, 0), at(10, 12, 7, 0), "later today"},
		{store.ReportSchedule{Every: "day", Minute: 7 * 60}, at(10, 12, 7, 0), at(10, 13, 7, 0), "not the very same minute"},
		{store.ReportSchedule{Every: "week", Weekday: 1, Minute: 8 * 60}, at(10, 12, 9, 0), at(10, 19, 8, 0), "Monday 12 past 08:00: the next Monday"},
		{store.ReportSchedule{Every: "week", Weekday: 1, Minute: 8 * 60}, at(10, 11, 9, 0), at(10, 12, 8, 0), "Sunday: tomorrow"},
		{store.ReportSchedule{Every: "month", Day: 1, Minute: 7 * 60}, at(10, 1, 7, 0), at(11, 1, 7, 0), "the 1st, next month"},
		{store.ReportSchedule{Every: "month", Day: -1, Minute: 22 * 60}, at(2, 3, 0, 0), at(2, 28, 22, 0), "the last day of February"},
		{store.ReportSchedule{Every: "month", Day: -1, Minute: 22 * 60}, at(2, 28, 23, 0), at(3, 31, 22, 0), "then of March"},
		{store.ReportSchedule{Every: ""}, at(2, 1, 0, 0), time.Time{}, "on request only"},
	} {
		if got := reportNext(c.sc, c.after, loc); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.describe, got, c.want)
		}
	}
}

func TestOccurrencesBetween(t *testing.T) {
	loc := time.UTC
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	// Saturdays at 23:00 for 2 hours: Sep 5, 12, 19 and 26 (Aug 29's ended before the period).
	w := store.MaintenanceWindow{Kind: "weekly", Weekdays: 1 << 6, Minute: 23 * 60, DurationMin: 120, Enabled: true}
	got := occurrencesBetween(w, from, to, loc)
	if len(got) != 4 || !got[0].From.Equal(time.Date(2026, 9, 5, 23, 0, 0, 0, loc)) || !got[3].To.Equal(time.Date(2026, 9, 27, 1, 0, 0, 0, loc)) {
		t.Fatalf("weekly: got %+v", got)
	}
	// A daily window at 23:30 for 2 hours: the one starting Aug 31 runs into the period.
	d := store.MaintenanceWindow{Kind: "daily", Minute: 23*60 + 30, DurationMin: 120, Enabled: true}
	if got := occurrencesBetween(d, from, to, loc); len(got) != 31 || !got[0].From.Equal(time.Date(2026, 8, 31, 23, 30, 0, 0, loc)) {
		t.Fatalf("daily: got %d, first %+v", len(got), got[0])
	}
	once := store.MaintenanceWindow{Kind: "once", StartAt: time.Date(2026, 10, 2, 0, 0, 0, 0, loc).Unix(), DurationMin: 60}
	if got := occurrencesBetween(once, from, to, loc); len(got) != 0 {
		t.Fatalf("a one-off after the period: got %+v", got)
	}
}

func TestCheckReport(t *testing.T) {
	idx := map[string]hostInfo{"10": {Name: "sw-core", Groups: []string{"site1/Network"}}, "20": {Name: "other", Groups: []string{"site2"}}}
	now, loc := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), time.UTC
	// A name is one line: a line break can't add a header to the email it heads.
	ok := reportBody{Name: " Monthly\r\nBcc: x@example.com ", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "last_month", Target: 99.9},
		Schedule: store.ReportSchedule{Every: "month", Day: 1, Minute: 420, To: []string{"it@example.com", " it@example.com "}, Enabled: true}}
	if err := checkReport(&ok, siteScope{sites: []string{"site1"}}, idx, now, loc, true); err != nil || ok.Name != "Monthly  Bcc: x@example.com" || len(ok.Schedule.To) != 1 {
		t.Fatalf("a valid report: %v %+v", err, ok)
	}
	for name, b := range map[string]reportBody{
		"no name":             {Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}},
		"nothing picked":      {Name: "x", Spec: store.ReportSpec{Period: "7d"}},
		"site out of scope":   {Name: "x", Spec: store.ReportSpec{Sites: []string{"site2"}, Period: "7d"}},
		"host out of scope":   {Name: "x", Spec: store.ReportSpec{Hosts: []string{"20"}, Period: "7d"}},
		"sensor out of scope": {Name: "x", Spec: store.ReportSpec{Sensors: []store.ReportSensor{{HostID: "20", Items: []string{"1"}}}, Period: "7d"}},
		"target over 100":     {Name: "x", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d", Target: 101}},
		"bad address":         {Name: "x", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}, Schedule: store.ReportSchedule{Every: "day", To: []string{"Bob <bob@example.com>"}, Enabled: true}},
		"scheduled, no one":   {Name: "x", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}, Schedule: store.ReportSchedule{Every: "week", Enabled: true}},
		"day 31":              {Name: "x", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}, Schedule: store.ReportSchedule{Every: "month", Day: 31}},
	} {
		if err := checkReport(&b, siteScope{sites: []string{"site1"}}, idx, now, loc, true); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// reportZabbix answers the calls a report makes: site1 holds sw-core (10, up all month) and web1
// (11, down two hours on Sep 12, one of them inside a maintenance window; an HTTPS response time
// sensor with a warning at 0.8 s and an error at 1.2 s); site2 holds a host the user can't see.
func reportZabbix(t *testing.T, from time.Time) *httptest.Server {
	hosts := []map[string]any{
		{"hostid": "10", "name": "sw-core", "status": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1/Network"}}},
		{"hostid": "11", "name": "web1", "status": "0", "hostgroups": []map[string]string{{"groupid": "2", "name": "site1/Servers"}}},
		{"hostid": "12", "name": "hidden", "status": "0", "hostgroups": []map[string]string{{"groupid": "3", "name": "site2"}}},
	}
	items := map[string]map[string]string{
		"100": {"itemid": "100", "hostid": "10", "name": "ICMP ping", "key_": "icmpping", "value_type": "3", "status": "0"},
		"110": {"itemid": "110", "hostid": "11", "name": "ICMP ping", "key_": "icmpping", "value_type": "3", "status": "0"},
		"120": {"itemid": "120", "hostid": "12", "name": "ICMP ping", "key_": "icmpping", "value_type": "3", "status": "0"},
		"111": {"itemid": "111", "hostid": "11", "name": "HTTPS response time", "key_": "web.time", "value_type": "0", "units": "s", "status": "0"},
	}
	down := from.Add(11*24*time.Hour + 3*time.Hour) // Sep 12, 03:00 and 04:00
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		ids := func() []string {
			var out []string
			switch v := req.Params["itemids"].(type) {
			case []any:
				for _, x := range v {
					out = append(out, x.(string))
				}
			case string:
				out = []string{v}
			}
			return out
		}
		switch req.Method {
		case "host.get":
			if _, ifaces := req.Params["selectInterfaces"]; ifaces {
				result = []map[string]any{{"hostid": "11", "interfaces": []map[string]string{{"ip": "10.0.0.21", "dns": "", "main": "1"}}}}
				break
			}
			result = hosts
		case "item.get":
			var out []map[string]string
			if f, ok := req.Params["filter"].(map[string]any); ok {
				for _, k := range f["key_"].([]any) {
					for _, it := range items {
						if it["key_"] == k {
							out = append(out, it)
						}
					}
				}
			}
			for _, id := range ids() {
				out = append(out, items[id])
			}
			result = out
		case "trend.get":
			var out []map[string]string
			for _, id := range ids() {
				for h := 0; h < 30*24; h++ {
					at := from.Add(time.Duration(h) * time.Hour)
					avg, lo, hi := "1", "1", "1"
					if id == "110" && (at.Equal(down) || at.Equal(down.Add(time.Hour))) {
						avg, lo = "0", "0"
					}
					if id == "111" {
						avg, lo, hi = "0.2", "0.1", "0.3"
						if at.Equal(down.Add(-24 * time.Hour)) {
							avg, hi = "0.9", "1.4"
						}
					}
					out = append(out, map[string]string{"itemid": id, "clock": strconv.FormatInt(at.Unix(), 10), "num": "60", "value_avg": avg, "value_min": lo, "value_max": hi})
				}
			}
			result = out
		case "trigger.get":
			if _, byItem := req.Params["itemids"]; byItem {
				result = []map[string]any{
					{"triggerid": "900", "status": "0", "priority": "2", "expression": "last(/web1/web.time)>0.8", "items": []map[string]string{{"itemid": "111"}}},
					{"triggerid": "901", "status": "0", "priority": "4", "expression": "last(/web1/web.time)>1.2", "items": []map[string]string{{"itemid": "111"}}},
				}
				break
			}
			result = []map[string]any{
				{"triggerid": "800", "items": []map[string]string{{"itemid": "110"}}},
				{"triggerid": "900", "items": []map[string]string{{"itemid": "111"}}},
			}
		case "event.get":
			if _, recov := req.Params["eventids"]; recov {
				result = []map[string]string{{"eventid": "601", "clock": strconv.FormatInt(down.Add(2*time.Hour).Unix(), 10)}, {"eventid": "602", "clock": strconv.FormatInt(down.Add(-24*time.Hour+35*time.Minute).Unix(), 10)}}
				break
			}
			host := []map[string]string{{"hostid": "11", "name": "web1", "status": "0"}}
			result = []map[string]any{
				{"eventid": "2", "clock": strconv.FormatInt(down.Unix(), 10), "name": "web1 is unavailable by ICMP ping", "severity": "4", "r_eventid": "601", "objectid": "800", "hosts": host},
				{"eventid": "1", "clock": strconv.FormatInt(down.Add(-24*time.Hour).Unix(), 10), "name": "HTTPS response time over 800 ms", "severity": "2", "r_eventid": "602", "objectid": "900", "hosts": host},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
}

func TestBuildReport(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mock := reportZabbix(t, from)
	defer mock.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	zbx := zabbix.New(mock.URL, "test-token")
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Set(ctx, map[string]string{settings.KeyTimezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	// Web1's maintenance: Sep 12 from 04:00 for an hour, the second hour it was down.
	if _, err := st.CreateMaintenanceWindow(ctx, store.MaintenanceWindow{Name: "patch", HostIDs: []string{"11"}, Kind: "once",
		StartAt: from.Add(11*24*time.Hour + 4*time.Hour).Unix(), DurationMin: 60, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, zbx: zbx, mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	spec := store.ReportSpec{Sites: []string{"site1"}, Hosts: []string{"12"}, Period: "last_month", Target: 99.9, SkipMaintenance: true,
		Sensors: []store.ReportSensor{{HostID: "11", Items: []string{"111"}, Label: "HTTPS response time"}}}
	doc, err := s.buildReport(ctx, reportRun{name: "Monthly availability, site1", spec: spec, scope: siteScope{sites: []string{"site1"}},
		now: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), to: []string{"it@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Period != "1 to 30 September 2026" || doc.For != "for it@example.com" || !strings.HasPrefix(doc.Made, "Made Oct 1, 2026 at 07:00 (UTC)") {
		t.Fatalf("cover: %q %q %q", doc.Period, doc.For, doc.Made)
	}
	// The host outside the user's site never shows, picked or not.
	if len(doc.Sections) != 2 || doc.Sections[0].Name != "sw-core" || doc.Sections[1].Name != "web1" {
		t.Fatalf("sections: %+v", doc.Sections)
	}
	// Web1 was down 2 hours, one of them in maintenance (left out): 1 hour of 719 counted.
	web := doc.Sections[1]
	if web.Uptime != "99.86 %" || web.UptimeTone != report.Err || web.Sub != "site1/Servers - 10.0.0.21" {
		t.Fatalf("web1 header: %q %v %q", web.Uptime, web.UptimeTone, web.Sub)
	}
	if len(web.Days) != 30 || web.Days[11].Tone != report.Err || web.Days[10].Tone != report.Good {
		t.Fatalf("web1 strip: %+v", web.Days)
	}
	if len(web.Problems) != 2 || web.Problems[0].Name != "HTTPS response time over 800 ms" || web.Problems[0].Lasted != "35 min" || web.Problems[1].Tone != report.Err {
		t.Fatalf("web1 problems: %+v", web.Problems)
	}
	if len(web.Charts) != 1 {
		t.Fatalf("web1 charts: %+v", web.Charts)
	}
	ch := web.Charts[0]
	if len(ch.Thresholds) != 2 || ch.Thresholds[0].V != 0.8 || ch.Thresholds[1].Tone != report.Err || len(ch.Series) != 1 || len(ch.Series[0].Points) != 720 {
		t.Fatalf("web1 chart: %+v", ch)
	}
	if ch.Foot != "in warning 35 min, in error 0" || ch.Label(0.8) != "800 ms" {
		t.Fatalf("web1 chart foot %q, label %q", ch.Foot, ch.Label(0.8))
	}
	if len(doc.Sensors) != 1 || doc.Sensors[0].Max != "1.4 s" || doc.Sensors[0].Min != "100 ms" || doc.Sensors[0].InWarn != "35 min" || doc.Sensors[0].WarnTone != report.Warn {
		t.Fatalf("sensor row: %+v", doc.Sensors)
	}
	// Page 1: two hosts, web1 under the target, an hour of downtime, two problems.
	if len(doc.Tiles) != 4 || doc.Tiles[1].Value != "1 host" || doc.Tiles[1].Sub != "web1, 99.86 %" || doc.Tiles[2].Value != "1 h" || doc.Tiles[3].Sub != "1 error, 1 warning" {
		t.Fatalf("tiles: %+v", doc.Tiles)
	}
	if len(doc.Hosts) != 2 || !doc.Hosts[1].Under || doc.Hosts[1].Longest != "2 h" || doc.Hosts[0].Uptime != "100 %" {
		t.Fatalf("hosts table: %+v", doc.Hosts)
	}
	if !strings.Contains(doc.Footnote, "Maintenance windows (1 h in all) are left out") {
		t.Fatalf("footnote: %q", doc.Footnote)
	}
	pdf, err := report.Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("REPORT_BUILD_OUT"); out != "" { // to look at it
		if err := os.WriteFile(out, pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A scheduled send that fails is tried again half an hour later, not every minute, and six hours past
// its time it waits for the next one; the error stays on the report for the list to show.
func TestReportTickRetries(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(ctx, store.User{Email: "ops@example.com", Name: "Ops", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	sched := store.ReportSchedule{Every: "day", Minute: 7 * 60, To: []string{"it@example.com"}, Enabled: true}
	id, err := st.CreateReport(ctx, store.Report{Name: "Daily", Spec: store.ReportSpec{Sites: []string{"site1"}, Period: "7d"}, Schedule: sched, OwnerID: uid})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, zbx: zabbix.New("", ""), mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	due := reportNext(sched, time.Now(), mgr.Location())
	state := func() store.Report {
		r, err := st.Report(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	s.reportTick(ctx, due.Add(-time.Minute))
	if r := state(); r.LastTryAt != 0 {
		t.Fatalf("not due yet, but tried: %+v", r)
	}
	s.reportTick(ctx, due.Add(time.Minute)) // no email channel: fails
	r := state()
	if r.LastTryAt == 0 || r.LastRunAt != 0 || !strings.Contains(r.LastError, "no email channel") {
		t.Fatalf("first try: %+v", r)
	}
	tried := r.LastTryAt
	s.reportTick(ctx, due.Add(10*time.Minute))
	if state().LastTryAt != tried {
		t.Fatal("tried again within half an hour")
	}
	s.reportTick(ctx, due.Add(40*time.Minute))
	if state().LastTryAt == tried {
		t.Fatal("not tried again after half an hour")
	}
	s.reportTick(ctx, due.Add(7*time.Hour))
	if r := state(); r.LastRunAt == 0 || r.LastSentAt != 0 || r.LastError == "" {
		t.Fatalf("six hours on it should wait for the next day, unsent: %+v", r)
	}
	if next := reportNext(sched, time.Unix(state().LastRunAt, 0), mgr.Location()); !next.After(due.Add(7 * time.Hour)) {
		t.Fatalf("next send %v", next)
	}
}
