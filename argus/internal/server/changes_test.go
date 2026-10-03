// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
)

func TestPatternValues(t *testing.T) {
	v := patternValues("DELETE /api/sensors/{key}/note", "/api/sensors/argus-interface-7/note")
	if v["key"] != "argus-interface-7" {
		t.Fatalf("key: %v", v)
	}
	if v := patternValues("POST /api/probes/{name}/update", "/api/probes/proxy%20site1/update"); v["name"] != "proxy site1" {
		t.Fatalf("escaped name: %v", v)
	}
	if v := patternValues("POST /api/hosts/{id}/pause", "/api/hosts/17"); len(v) != 0 {
		t.Fatalf("a path of another shape matched: %v", v)
	}
}

func TestHumanDur(t *testing.T) {
	for in, want := range map[int64]string{20: "1 min", 900: "15 min", 3600: "1 h", 5400: "1 h 30 min", 86400: "1 d", 90000: "1 d 1 h"} {
		if got := humanDur(in); got != want {
			t.Errorf("humanDur(%d) = %q, want %q", in, got, want)
		}
	}
}

// waitChanges polls the log until it has n entries (the middleware writes them in the background).
func waitChanges(t *testing.T, st *store.Store, n int) []store.Change {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		cs, err := st.ListChanges(t.Context(), store.ChangeQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) >= n || time.Now().After(deadline) {
			return cs
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A successful signed-in write is logged once, in its route's words or its handler's; a failed one,
// a read and an unlisted route aren't. The typed reason rides along, an ack's note stands in for one.
func TestChangeLogMiddleware(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{st: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/groups", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // the handler still reads the whole body
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a name is required"})
			return
		}
		changeObject(r, req.Name)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/events/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		changeObject(r, "web1 · Reachable (ICMP)", "17")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("PUT /api/thresholds/default", func(w http.ResponseWriter, r *http.Request) {
		noteChange(r, store.Change{Category: "thresholds", Action: "Changed a default threshold", Object: "Argus Ping",
			Diff: []store.ChangeDiff{{Field: "Loss warning", Old: "20 %", New: "10 %"}}})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("PATCH /api/settings", func(w http.ResponseWriter, r *http.Request) {
		skipChange(r) // nothing changed
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/groups", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, []string{}) })
	mux.HandleFunc("POST /api/me/notify/channels/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	})
	s.mux = mux
	h := s.changeLog(mux)
	alice := &store.User{ID: 7, Email: "alice@example.com", Name: "Alice", Surname: "Rossi", Role: "admin"}
	do := func(method, path, body string, hdr map[string]string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		req = req.WithContext(auth.WithUser(req.Context(), alice))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if c := do("POST", "/api/groups", `{"name":""}`, nil); c != 400 {
		t.Fatalf("bad create answered %d", c)
	}
	do("GET", "/api/groups", "", nil)
	do("POST", "/api/me/notify/channels/3/test", `{}`, nil)
	do("PATCH", "/api/settings", `{"values":{}}`, nil)
	do("POST", "/api/groups", `{"name":"site4"}`, map[string]string{changeReasonHeader: url.QueryEscape("new branch office, opens Monday")})
	do("POST", "/api/events/123/ack", `{"message":"on it","duration_seconds":7200}`, nil)
	do("PUT", "/api/thresholds/default", `{"template":"Argus Ping","macro":"{$PING.LOSS.WARN}","value":"10"}`, nil)

	cs := waitChanges(t, st, 3)
	if len(cs) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(cs), cs)
	}
	by := map[string]store.Change{}
	for _, c := range cs {
		by[c.Action] = c
	}
	thr, ack, grp := by["Changed a default threshold"], by["Acknowledged"], by["Created a group"]
	if grp.Action != "Created a group" || grp.Object != "site4" || grp.Actor != "Alice Rossi" || grp.ActorID != 7 || grp.Reason != "new branch office, opens Monday" || grp.Category != "groups" {
		t.Fatalf("group entry: %+v", grp)
	}
	if ack.Action != "Acknowledged" || ack.Detail != "for 2 h" || ack.Reason != "on it" || len(ack.HostIDs) != 1 || ack.HostIDs[0] != "17" {
		t.Fatalf("ack entry: %+v", ack)
	}
	if thr.Object != "Argus Ping" || len(thr.Diff) != 1 || thr.Diff[0].New != "10 %" || thr.RequestID == "" {
		t.Fatalf("threshold entry (the handler's own words): %+v", thr)
	}
}

// A host settings save reads as the fields it changed; a secret macro only says it changed.
func TestHostConfigDiff(t *testing.T) {
	before := hostConfigView{Host: "sw-floor2", Name: "sw-floor2", MonitoredBy: 1, ProxyID: "10",
		Interfaces: []ifaceView{{Type: 2, UseIP: 1, IP: "10.0.0.3", Port: "161", Inherit: true}},
		Macros:     []macroFieldView{{Macro: "{$UNIFI.SITE}", Label: "Site", Value: "default"}, {Macro: "{$UNIFI.KEY}", Label: "API key", Secret: true, Set: true}},
		Thresholds: []thresholdFieldView{{Macro: "{$TEMP.WARN}", Label: "Temperature warning", Unit: "°C", Default: "70"}},
		AddOns:     []addOnView{{ID: "http", Label: "HTTP/HTTPS endpoint", Enabled: false}},
	}
	order := []string{"Ping", "System"}
	master := "none"
	req := hostConfigUpdate{Host: "sw-floor2", Name: "Switch floor 2", MonitoredBy: 1, ProxyID: "11",
		Interfaces: before.Interfaces,
		Macros:     map[string]string{"{$UNIFI.SITE}": "default", "{$UNIFI.KEY}": "new-secret-value", "{$TEMP.WARN}": "65"},
		AddOns:     map[string]addOnDesired{"http": {Enabled: true}}, CategoryOrder: &order, Master: &master}
	before.Master = &masterView{ItemID: "900", DefaultID: "900", Options: []masterOption{{ID: "900", Label: "Reachable (ICMP ping)"}}}
	got := map[string][2]string{}
	for _, d := range hostConfigDiff(before, req, map[string]string{"10": "proxy-site1", "11": "proxy-site2"}) {
		got[d.Field] = [2]string{d.Old, d.New}
	}
	want := map[string][2]string{
		"Visible name":        {"sw-floor2", "Switch floor 2"},
		"Monitored by":        {"proxy-site1", "proxy-site2"},
		"API key":             {"", "changed"},
		"Temperature warning": {"default (70 °C)", "65 °C"},
		"HTTP/HTTPS endpoint": {"off", "on"},
		"Sensor order":        {"the default order", "Ping, System"},
		"Master sensor":       {"Reachable (ICMP ping)", "none"},
	}
	if len(got) != len(want) {
		t.Fatalf("diff fields: got %v, want %v", got, want)
	}
	for f, w := range want {
		if got[f] != w {
			t.Errorf("%s: got %v, want %v", f, got[f], w)
		}
	}
	for _, v := range got {
		if strings.Contains(v[1], "new-secret-value") {
			t.Fatal("a secret's value reached the change log")
		}
	}
}

// The probe and group filters: several of each, a group covering its subgroups, the server as "0".
func TestHostFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/changes?probe=10,0&group=site1&group=site2%2C+annex", nil)
	f := parseHostFilter(r)
	if len(f.probes) != 2 || len(f.groups) != 2 || f.groups[1] != "site2, annex" {
		t.Fatalf("parsed %+v", f)
	}
	for _, c := range []struct {
		h    hostInfo
		want bool
	}{
		{hostInfo{ProxyID: "10", Groups: []string{"site1/Network"}}, true},
		{hostInfo{ProxyID: "0", Groups: []string{"site2, annex"}}, true},
		{hostInfo{ProxyID: "11", Groups: []string{"site1"}}, false},
		{hostInfo{ProxyID: "10", Groups: []string{"site10"}}, false},
	} {
		if got := f.matches(c.h); got != c.want {
			t.Errorf("%+v: got %v", c.h, got)
		}
	}
	if (hostFilter{}).active() {
		t.Fatal("an empty filter filters")
	}
}
