// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// tickProblem is one open Zabbix problem in a notifier run: its trigger is on one sensor.
type tickProblem struct {
	event, name, itemID, key, sev string
	clock                         int64 // when it began (0 = a quarter of an hour ago)
}

// tickEnv is a fake Zabbix and a fresh store for whole notifier passes: an AdGuard host (id 10,
// site1) with its ping (item 100) and the collector's reachability sensor (item 101), whose last
// readings are ping and running, and the problems open (begun a quarter of an hour ago unless they
// say otherwise), which a test may change between passes. One alert channel serves everything.
type tickEnv struct {
	t        *testing.T
	st       *store.Store
	zbx      *zabbix.Client
	mgr      *settings.Manager
	logger   *slog.Logger
	problems []tickProblem
}

// lastTickStore is the store runNotifier last ran on, for a test to read what it recorded.
var lastTickStore *store.Store

func newTickEnv(t *testing.T, ping, running string, problems []tickProblem) *tickEnv {
	t.Helper()
	// Each run is a fresh install: no master seen coming back on an earlier one.
	masterClock.Lock()
	masterClock.down, masterClock.back = map[string]bool{}, map[string]int64{}
	masterClock.Unlock()
	e := &tickEnv{t: t, problems: problems}
	now := time.Now().Unix()
	host := []map[string]string{{"hostid": "10", "name": "adguard1", "status": "0"}}
	items := map[string]map[string]string{
		"100": {"itemid": "100", "hostid": "10", "name": "ICMP ping", "key_": "icmpping", "lastvalue": ping, "lastclock": itoa64(now - 30), "status": "0", "state": "0"},
		"101": {"itemid": "101", "hostid": "10", "name": "AdGuard running", "key_": "adguard.running", "lastvalue": running, "lastclock": itoa64(now - 30), "status": "0", "state": "0"},
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		switch req.Method {
		case "problem.get":
			var out []map[string]string
			for i, p := range e.problems {
				clock := p.clock
				if clock == 0 {
					clock = now - 900
				}
				out = append(out, map[string]string{"eventid": p.event, "name": p.name, "severity": p.sev, "clock": itoa64(clock), "acknowledged": "0", "objectid": itoa64(int64(900 + i))})
			}
			result = out
		case "trigger.get":
			var out []map[string]any
			for i, p := range e.problems {
				out = append(out, map[string]any{
					"triggerid": itoa64(int64(900 + i)), "expression": "max(/adguard1/" + p.key + ",#3)=0",
					"hosts": host, "items": []map[string]string{{"itemid": p.itemID, "key_": p.key}},
				})
			}
			result = out
		case "host.get":
			result = []map[string]any{{"hostid": "10", "name": "adguard1", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1"}}}}
		case "item.get":
			if f, ok := req.Params["filter"].(map[string]any); ok {
				if _, many := f["key_"].([]any); many { // the master sensors, by key
					result = []map[string]string{items["100"], items["101"]}
				}
				break
			}
			if ids, ok := req.Params["itemids"].([]any); ok {
				var out []map[string]string
				for _, id := range ids {
					if it, found := items[id.(string)]; found {
						out = append(out, it)
					}
				}
				result = out
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	t.Cleanup(mock.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lastTickStore = st
	ctx := t.Context()
	e.st, e.zbx = st, zabbix.New(mock.URL, "test-token")
	t.Setenv("ARGUS_ALERT_DELAY_SECONDS", "0") // alert as soon as a problem is seen twice
	if e.mgr, err = settings.New(ctx, st, zabbix.New("", "")); err != nil {
		t.Fatal(err)
	}
	// An install that has been alerting for a while: no first-run baseline swallows these problems.
	for _, k := range []string{notifyBaselineKey, notifySynthBaselineKey, notifyDeliveriesKey} {
		_ = st.MetaSet(ctx, k, "1")
	}
	// A channel for every site and warnings up, told at once. It has no webhook, so each send fails
	// fast - what counts here is who the notifier decided to tell.
	if _, err := st.CreateNotifyChannel(ctx, store.NotifyChannel{Type: "discord", Name: "ops", Enabled: true, MinSeverity: 2, Alerts: true, Config: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	e.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return e
}

func (e *tickEnv) tick() { notifyTick(e.t.Context(), e.st, e.zbx, e.logger, e.mgr, "secret") }

// sent is the events that reached the alert channel.
func (e *tickEnv) sent() []string {
	got, err := e.st.NotifyDeliveries(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for eid, byDest := range got {
		if len(byDest) > 0 {
			out = append(out, eid)
		}
	}
	sort.Strings(out)
	return out
}

// timeline is an incident's timeline entries as kind (": detail").
func (e *tickEnv) timeline(eventID string) []string {
	entries, err := e.st.IncidentLog(e.t.Context(), eventID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, x := range entries {
		out = append(out, x.Kind+": "+x.Detail)
	}
	return out
}

// runNotifier runs three passes over the problems and returns the events that reached the channel.
func runNotifier(t *testing.T, ping, running string, problems []tickProblem) []string {
	t.Helper()
	e := newTickEnv(t, ping, running, problems)
	// First pass: the problems are new, and wait for the next pass. Then whatever is due goes out.
	e.tick()
	if states, _ := e.st.NotifyStates(t.Context()); len(states) != len(problems) {
		t.Fatalf("want every problem tracked after the first pass, got %d of %d", len(states), len(problems))
	}
	e.tick()
	e.tick() // a held alert stays held: nothing new on a later pass
	return e.sent()
}

// A device that goes down alerts once, as "unavailable" - never not at all. Its ping and its
// collector are both down then, and each is a master of the host's other sensors: neither may hold
// the ping's own alert back.
func TestNotifierDeviceDown(t *testing.T) {
	pingDown := tickProblem{"e1", "Unavailable by ICMP ping", "100", "icmpping", "4", 0}
	serviceDown := tickProblem{"e2", "AdGuard Home is down or unreachable", "101", "adguard.running", "4", 0}
	httpDown := tickProblem{"e3", "HTTP/HTTPS endpoint down", "102", "net.tcp.service[http,,80]", "4", 0}

	// The whole machine is down: one alert, the ping's.
	if sent := runNotifier(t, "0", "0", []tickProblem{pingDown, serviceDown, httpDown}); strings.Join(sent, ",") != "e1" {
		t.Fatalf("machine down: want only the ping alert sent, got %v", sent)
	}
	// The service stopped, the machine still answers: one alert, the collector's.
	if sent := runNotifier(t, "1", "0", []tickProblem{serviceDown, httpDown}); strings.Join(sent, ",") != "e2" {
		t.Fatalf("service down: want only the collector alert sent, got %v", sent)
	}
	// Only the web endpoint fails: it alerts on its own.
	if sent := runNotifier(t, "1", "1", []tickProblem{httpDown}); strings.Join(sent, ",") != "e3" {
		t.Fatalf("endpoint down: want its alert sent, got %v", sent)
	}
}

// The incident timeline records what the notifier did: the incident opened, and the channel it tried
// to alert (here it can't: the test channel has no webhook, so the timeline says why).
func TestNotifierTimeline(t *testing.T) {
	runNotifier(t, "1", "1", []tickProblem{{event: "7001", name: "AdGuard is not running", sev: "4", key: "adguard.running", itemID: "101"}})
	entries, err := lastTickStore.IncidentLog(t.Context(), "7001")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind+":"+e.Channel)
	}
	if len(entries) < 2 || entries[0].Kind != "opened" || entries[0].Detail != "High: AdGuard is not running" || entries[1].Kind != "send_failed" || entries[1].Channel != "ops (Discord)" || entries[1].Detail == "" {
		t.Fatalf("timeline: %v %+v", kinds, entries)
	}
}

// A probe cut off from the core keeps checking and stores the readings; as it reconnects it sends them,
// and Zabbix opens the problems they show, dated when they began, which the next live check closes.
// Argus hears of those late: their alerts wait out the settle window, and one that closes meanwhile
// sends nothing at all, neither the problem nor its recovery.
func TestNotifierLateProblemClosesQuietly(t *testing.T) {
	now := time.Now().Unix()
	saas := tickProblem{event: "e9", name: "Cloudflare does not answer from this site", itemID: "102", key: "saas.up[1]", sev: "4", clock: now - 22*60}
	e := newTickEnv(t, "1", "1", []tickProblem{saas})
	_ = e.st.MetaSet(t.Context(), notifyLastReadKey, itoa64(now-30)) // the last read, half a minute ago, didn't have it
	e.tick()
	e.tick()
	e.tick()
	if sent := e.sent(); len(sent) != 0 {
		t.Fatalf("a problem heard of late alerted before the settle window was over: %v", sent)
	}
	e.problems = nil // the next live check closes it
	e.tick()
	tl := e.timeline("e9")
	if len(tl) != 2 || tl[0] != "late: 22m" || tl[1] != "resolved: it closed while its alert waited, so nothing was sent" {
		t.Fatalf("timeline: %q", tl)
	}
}

// A problem heard of late that is still open once the settle window is over alerts as usual: waiting
// never silences what is really down.
func TestNotifierLateProblemStillOpen(t *testing.T) {
	now := time.Now().Unix()
	saas := tickProblem{event: "e9", name: "Cloudflare does not answer from this site", itemID: "102", key: "saas.up[1]", sev: "4", clock: now - 22*60}
	e := newTickEnv(t, "1", "1", []tickProblem{saas})
	ctx := t.Context()
	_ = e.st.MetaSet(ctx, notifyLastReadKey, itoa64(now-30))
	e.tick()
	states, _ := e.st.NotifyStates(ctx)
	s := states["e9"]
	if !s.Late {
		t.Fatalf("want e9 heard of late: %+v", s)
	}
	s.FirstSeen -= 5*60 + 1 // the settle window (5 minutes by default) has passed since
	_ = e.st.DeleteNotifyState(ctx, "e9")
	_ = e.st.UpsertNotifyState(ctx, s)
	e.tick()
	if sent := e.sent(); strings.Join(sent, ",") != "e9" {
		t.Fatalf("a problem heard of late and still open after the settle window must alert, got %v", sent)
	}
}

// A problem Argus sees on time, or one that began while Argus wasn't reading (no read recorded, as on a
// fresh install), isn't late: it alerts after the alert delay as before.
func TestNotifierOnTimeProblemNotLate(t *testing.T) {
	now := time.Now().Unix()
	fresh := tickProblem{event: "e8", name: "Cloudflare does not answer from this site", itemID: "102", key: "saas.up[1]", sev: "4", clock: now - 60}
	e := newTickEnv(t, "1", "1", []tickProblem{fresh})
	_ = e.st.MetaSet(t.Context(), notifyLastReadKey, itoa64(now-30))
	e.tick()
	e.tick()
	if sent := e.sent(); strings.Join(sent, ",") != "e8" {
		t.Fatalf("a problem seen on time: want it alerted, got %v", sent)
	}
	if sent := runNotifier(t, "1", "1", []tickProblem{{event: "e7", name: "x", itemID: "102", key: "saas.up[1]", sev: "4"}}); strings.Join(sent, ",") != "e7" {
		t.Fatalf("no earlier read: want it alerted, got %v", sent)
	}
}

// The settle window after a master comes back counts from when the notifier saw it come back, even
// when Zabbix dates the recovery earlier (here it gives no time at all): a probe reconnecting closes
// its "no data" trigger with a stored reading taken minutes before it arrived.
func TestNotifierSettlesFromWhenItSawTheMasterBack(t *testing.T) {
	run := func() []string {
		now := time.Now().Unix()
		pingDown := tickProblem{"e1", "Unavailable by ICMP ping", "100", "icmpping", "4", 0}
		// On time (not heard of late) and after the ping's last reading (no wait for the master to report).
		serviceDown := tickProblem{event: "e2", name: "AdGuard Home is down or unreachable", itemID: "101", key: "adguard.running", sev: "4", clock: now - 60}
		e := newTickEnv(t, "1", "0", []tickProblem{pingDown})
		e.tick()
		e.problems = []tickProblem{serviceDown} // the ping is back, the service still fails
		e.tick()
		e.tick()
		e.tick()
		return e.sent()
	}
	if sent := run(); len(sent) != 0 {
		t.Fatalf("the service alert must wait out the settle window after the ping came back, got %v", sent)
	}
	t.Setenv("ARGUS_SETTLE_SECONDS", "0") // no settle window: it alerts
	if sent := run(); strings.Join(sent, ",") != "e2" {
		t.Fatalf("settle off: want the service alert sent, got %v", sent)
	}
}
