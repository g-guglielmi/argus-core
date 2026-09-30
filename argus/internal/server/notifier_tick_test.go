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
}

// runNotifier drives whole notifier passes against a fake Zabbix: an AdGuard host (id 10, site1)
// with its ping (item 100) and the collector's reachability sensor (item 101), whose last readings
// are ping and running, and the given problems open for a quarter of an hour. It returns the events
// that reached the one alert channel.
func runNotifier(t *testing.T, ping, running string, problems []tickProblem) []string {
	t.Helper()
	now := time.Now().Unix()
	clock := itoa64(now - 900)
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
			for i, p := range problems {
				out = append(out, map[string]string{"eventid": p.event, "name": p.name, "severity": p.sev, "clock": clock, "acknowledged": "0", "objectid": itoa64(int64(900 + i))})
			}
			result = out
		case "trigger.get":
			var out []map[string]any
			for i, p := range problems {
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
	defer mock.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	zbx := zabbix.New(mock.URL, "test-token")
	t.Setenv("ARGUS_ALERT_DELAY_SECONDS", "0") // alert as soon as a problem is seen twice
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First pass: the problems are new, and wait for the next pass. Then whatever is due goes out.
	notifyTick(ctx, st, zbx, logger, mgr, "secret")
	if states, _ := st.NotifyStates(ctx); len(states) != len(problems) {
		t.Fatalf("want every problem tracked after the first pass, got %d of %d", len(states), len(problems))
	}
	notifyTick(ctx, st, zbx, logger, mgr, "secret")
	notifyTick(ctx, st, zbx, logger, mgr, "secret") // a held alert stays held: nothing new on a later pass

	got, err := st.NotifyDeliveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	for eid, byDest := range got {
		if len(byDest) > 0 {
			sent = append(sent, eid)
		}
	}
	sort.Strings(sent)
	return sent
}

// A device that goes down alerts once, as "unavailable" - never not at all. Its ping and its
// collector are both down then, and each is a master of the host's other sensors: neither may hold
// the ping's own alert back.
func TestNotifierDeviceDown(t *testing.T) {
	pingDown := tickProblem{"e1", "Unavailable by ICMP ping", "100", "icmpping", "4"}
	serviceDown := tickProblem{"e2", "AdGuard Home is down or unreachable", "101", "adguard.running", "4"}
	httpDown := tickProblem{"e3", "HTTP/HTTPS endpoint down", "102", "net.tcp.service[http,,80]", "4"}

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
