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
	"strings"
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// A collector never holds the ping's own sensors: packet loss while the service is down alerts on its
// own, while the ping (down) still holds it.
func TestMasterHoldPingFamily(t *testing.T) {
	loss := []masterRef{{id: "303", key: "icmppingloss"}}
	if v := testMasters("301").hold("30", loss, 990, 1100); v.held {
		t.Fatalf("the collector held the ping's loss: %+v", v)
	}
	if v := testMasters("300").hold("30", loss, 990, 1100); !v.held || v.master.itemID != "300" || v.host != "30" {
		t.Fatalf("the ping must hold its loss, naming itself: %+v", v)
	}
	// The verdict names the master: the probe for a site hold.
	if v := testMasters("700").hold("10", []masterRef{{id: "101", key: "system.cpu.util"}}, 990, 1100); v.master.itemID != "700" || v.host != "70" {
		t.Fatalf("site hold should name the Probe host's sensor: %+v", v)
	}
}

// censusFixture is a fake Zabbix with an AdGuard host (10) - its ping (100), loss (103), the
// collector's reachability (101) and a DNS check (104) - and a switch (20) whose loss (203) warns. The
// problems are the given events, keyed by the sensor they are on.
func censusFixture(t *testing.T, ping, running string, open map[string]string) *Server {
	t.Helper()
	type sensor struct{ id, host, name, key, value string }
	sensors := []sensor{
		{"100", "10", "ICMP ping", "icmpping", ping},
		{"103", "10", "ICMP loss", "icmppingloss", "100"},
		{"101", "10", "AdGuard running", "adguard.running", running},
		{"104", "10", "Resolves google.com", "dns.resolve.success[google.com]", "0"},
		{"200", "20", "ICMP ping", "icmpping", "1"},
		{"203", "20", "ICMP loss", "icmppingloss", "30"},
	}
	hostName := map[string]string{"10": "adguard1", "20": "switch1"}
	sev := map[string]string{"203": "2"} // the switch's loss warns; the rest are errors
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			ID     any            `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		row := func(s sensor) map[string]any {
			return map[string]any{"itemid": s.id, "hostid": s.host, "name": s.name, "key_": s.key, "lastvalue": s.value, "lastclock": "1000",
				"value_type": "3", "status": "0", "state": "0", "hosts": []map[string]string{{"hostid": s.host, "name": hostName[s.host], "status": "0"}}}
		}
		switch req.Method {
		case "problem.get":
			var out []map[string]string
			for item, name := range open {
				s := sev[item]
				if s == "" {
					s = "4"
				}
				out = append(out, map[string]string{"eventid": "e" + item, "name": name, "severity": s, "clock": "900", "acknowledged": "0", "objectid": "t" + item})
			}
			result = out
		case "trigger.get":
			var out []map[string]any
			for _, s := range sensors {
				if _, on := open[s.id]; on {
					out = append(out, map[string]any{"triggerid": "t" + s.id, "expression": "max(/h/" + s.key + ",#3)=0",
						"hosts": []map[string]string{{"hostid": s.host, "name": hostName[s.host], "status": "0"}}, "items": []map[string]string{{"itemid": s.id, "key_": s.key}}})
				}
			}
			result = out
		case "host.get":
			result = []map[string]any{
				{"hostid": "10", "name": "adguard1", "status": "0", "proxyid": "0"},
				{"hostid": "20", "name": "switch1", "status": "0", "proxyid": "0"},
			}
		case "item.get":
			f, filtered := req.Params["filter"].(map[string]any)
			switch {
			case filtered:
				if keys, many := f["key_"].([]any); many { // the master sensors, by key
					var out []map[string]any
					for _, s := range sensors {
						for _, k := range keys {
							if k == s.key {
								out = append(out, row(s))
							}
						}
					}
					result = out
				}
			case req.Params["itemids"] != nil:
			default: // every item, with its host
				var out []map[string]any
				for _, s := range sensors {
					out = append(out, row(s))
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
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.census = newCensusCache(s.buildCensus)
	return s
}

// When a device's master is down, the census names it on every sensor it holds and counts them apart,
// so the error list shows one incident per dead device.
func TestCensusHeld(t *testing.T) {
	byItem := func(rows []sensorRow) map[string]sensorRow {
		out := map[string]sensorRow{}
		for _, r := range rows {
			out[r.ItemID] = r
		}
		return out
	}

	// The machine is down: the ping holds the loss, the collector and the DNS check.
	s := censusFixture(t, "0", "0", map[string]string{
		"100": "Unavailable by ICMP ping", "103": "Severe ICMP packet loss", "101": "AdGuard Home is down or unreachable",
		"104": "DNS not resolving google.com", "203": "High ICMP packet loss",
	})
	rows, err := s.buildCensus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := byItem(rows)
	if p := got["100"]; p.State != "error" || p.HeldBy != nil || p.Holds != 3 {
		t.Fatalf("ping row: state %s held %+v holds %d (want error, not held, holding 3)", p.State, p.HeldBy, p.Holds)
	}
	for _, id := range []string{"103", "101", "104"} {
		h := got[id].HeldBy
		if h == nil || h.ItemID != "100" || h.HostID != "10" || h.HostName != "adguard1" || h.Name != "Reachable (ICMP)" {
			t.Fatalf("sensor %s should be held by the ping, got %+v", id, h)
		}
	}
	if sw := got["203"]; sw.State != "warning" || sw.HeldBy != nil {
		t.Fatalf("another host's warning is not held: %+v", sw)
	}

	// The pills count the ping and the switch; the three held apart. The held rows still come with
	// their state, for the list's "show held".
	req := httptest.NewRequest(http.MethodGet, "/api/census?rows=error,warning", nil)
	rec := httptest.NewRecorder()
	s.handleCensus(rec, req)
	var resp struct {
		Counts map[string]int `json:"counts"`
		Rows   []sensorRow    `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Counts["error"] != 1 || resp.Counts["warning"] != 1 || resp.Counts["held"] != 3 || len(resp.Rows) != 5 {
		t.Fatalf("counts %v with %d rows (want error 1, warning 1, held 3, 5 rows)", resp.Counts, len(resp.Rows))
	}

	// The service stopped, the machine answers: the collector holds the DNS check, not the ping's loss.
	s = censusFixture(t, "1", "0", map[string]string{
		"103": "Severe ICMP packet loss", "101": "AdGuard Home is down or unreachable", "104": "DNS not resolving google.com",
	})
	rows, err = s.buildCensus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got = byItem(rows)
	if c := got["101"]; c.HeldBy != nil || c.Holds != 1 {
		t.Fatalf("collector row: held %+v holds %d (want not held, holding 1)", c.HeldBy, c.Holds)
	}
	if h := got["104"].HeldBy; h == nil || h.ItemID != "101" {
		t.Fatalf("the DNS check should be held by the collector, got %+v", h)
	}
	if l := got["103"]; l.HeldBy != nil || l.State != "error" {
		t.Fatalf("the ping's loss is not the collector's to hold: %+v", l)
	}

	// Nothing down: nothing held.
	s = censusFixture(t, "1", "1", map[string]string{"203": "High ICMP packet loss"})
	rows, _ = s.buildCensus(t.Context())
	for _, r := range rows {
		if r.HeldBy != nil || r.Holds != 0 {
			t.Fatalf("nothing is down, yet %s is marked: %+v", r.ItemID, r)
		}
	}

	// An OK row carries an empty event list, not null: the OK list reads its length (a null blanked the page).
	req = httptest.NewRequest(http.MethodGet, "/api/census?rows=ok", nil)
	rec = httptest.NewRecorder()
	s.handleCensus(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `"event_ids":null`) || !strings.Contains(body, `"state":"ok"`) || !strings.Contains(body, `"event_ids":[]`) {
		t.Fatalf("OK rows should carry \"event_ids\":[], got %s", body)
	}
}
