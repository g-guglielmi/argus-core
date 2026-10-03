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
	"reflect"
	"strings"
	"testing"

	"argus/internal/auth"
	"argus/internal/notify"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestSiteScopeRules(t *testing.T) {
	all := scopeOf(&store.User{Role: "viewer"})
	admin := scopeOf(&store.User{Role: "admin", Sites: []string{"site1"}})
	if !all.all || !admin.all {
		t.Fatal("no sites, or an admin, sees every site")
	}
	sc := scopeOf(&store.User{Role: "helpdesk", Sites: []string{"site1", "site2/Network"}})
	for g, want := range map[string]bool{"site1": true, "site1/Office": true, "site10": false, "site2": false, "site2/Network": true, "site2/Network/Core": true, "site3": false} {
		if got := sc.coversGroup(g); got != want {
			t.Errorf("coversGroup(%q) = %v, want %v", g, got, want)
		}
	}
	if !sc.showsGroup("site2") || sc.showsGroup("site3") {
		t.Error("the path down to a scope site shows, other groups don't")
	}
	if !sc.sees([]string{"site3", "site1/Office"}) || sc.sees([]string{"site3"}) {
		t.Error("a host is visible when any of its groups is in scope")
	}
	for _, c := range []struct {
		in, want []string
	}{
		{nil, []string{"site1", "site2/Network"}},            // every site -> my sites
		{[]string{"site1/Office"}, []string{"site1/Office"}}, // narrower stays
		{[]string{"site2"}, []string{"site2/Network"}},       // broader becomes mine
		{[]string{"site3"}, []string{}},                      // disjoint -> none
		{[]string{"site1", "site3"}, []string{"site1"}},      // partly mine
	} {
		if got := sc.narrow(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("narrow(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if got := all.narrow([]string{"site3"}); !reflect.DeepEqual(got, []string{"site3"}) {
		t.Errorf("an unscoped user's list is untouched, got %v", got)
	}
	if outsideSites(sc, []string{"site1/Office"}) != "" || outsideSites(sc, nil) != "" {
		t.Error("my own sites (or all of them) are fine")
	}
	if msg := outsideSites(sc, []string{"site3"}); !strings.Contains(msg, "site3") {
		t.Errorf("site3 should be refused, got %q", msg)
	}
}

// A scoped user's personal channel serves only their sites; the email-to-users fan-out reaches a
// scoped user only with their sites' alerts and probe notices, never news about the whole install.
func TestNotifierHonoursScope(t *testing.T) {
	dir := userDirectory{scopes: map[int64]siteScope{
		1: {all: true},
		2: {sites: []string{"site1"}},
	}}
	chans := []store.UserNotifyChannel{
		{ID: 10, UserID: 1, Alerts: true},
		{ID: 11, UserID: 2, Alerts: true},
		{ID: 12, UserID: 2, Alerts: true, Sites: []string{"site2"}},
		{ID: 13, UserID: 3, Alerts: true}, // a user the directory doesn't know
	}
	dests := notifyDests(nil, nil, chans, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	serves := map[int64]bool{}
	for _, d := range dests {
		serves[d.id] = d.serves(hostRoute{groups: []string{"site2/Office"}}, 4)
	}
	if !serves[10] || serves[11] || serves[12] || serves[13] {
		t.Fatalf("site2 alert reached %v; want only the unscoped user's channel", serves)
	}
	for _, d := range dests {
		if d.id == 11 && !d.serves(hostRoute{groups: []string{"site1/Office"}}, 4) {
			t.Fatal("the scoped user's channel should serve their own site")
		}
	}
	scoped := userRecipient{email: "local@example.com", scope: siteScope{sites: []string{"site1"}}}
	if !scoped.reaches(notify.Event{Groups: []string{"site1/Office"}}) || scoped.reaches(notify.Event{Groups: []string{"site2"}}) {
		t.Error("alerts: only the user's sites")
	}
	if !scoped.reaches(notify.Event{Kind: "info", Site: "site1"}) || scoped.reaches(notify.Event{Kind: "info"}) {
		t.Error("notices: the user's probes yes, the whole install no")
	}
	n := notice{key: "update", title: "Update available"}
	for _, d := range noticeTargets(n, []notifyDest{{id: 1, notices: true}, {id: 2, notices: true, scoped: true, sites: []string{"site1"}}}) {
		if d.id == 2 {
			t.Error("a scoped user's channel got a notice about the whole install")
		}
	}
}

func TestScopedOrderSets(t *testing.T) {
	sc := siteScope{sites: []string{"site1"}}
	vis := map[string]bool{"101": true}
	got := scopedOrderSets(sc, vis, []store.OrderSet{
		{Scope: "", Kind: "group", Items: []string{"site2", "site1"}},
		{Scope: "site1", Kind: "sibling", Items: []string{"201", "site1/Office", "101"}},
		{Scope: "site2", Kind: "host", Items: []string{"301"}},
	})
	want := []store.OrderSet{
		{Scope: "", Kind: "group", Items: []string{"site1"}},
		{Scope: "site1", Kind: "sibling", Items: []string{"site1/Office", "101"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order sets = %+v", got)
	}
}

// The API, end to end against a fake Zabbix: a user limited to site1 lists only site1's hosts, gets
// 404 for another site's host, sensor or problem, and sees only site1's groups; an admin sees all.
func TestScopedAPI(t *testing.T) {
	hosts := []map[string]any{
		{"hostid": "101", "name": "nas1", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1"}}},
		{"hostid": "102", "name": "sw1", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "3", "name": "site1/Network"}}},
		{"hostid": "201", "name": "fw2", "status": "0", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "2", "name": "site2"}}},
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
		case "host.get":
			result = hosts
		case "item.get":
			var out []map[string]string
			ids, _ := req.Params["itemids"].([]any)
			for _, id := range ids {
				host := "101"
				if strings.HasPrefix(id.(string), "2") {
					host = "201"
				}
				out = append(out, map[string]string{"itemid": id.(string), "hostid": host, "name": "x", "key_": "k", "value_type": "0"})
			}
			if one, ok := req.Params["itemids"].(string); ok {
				host := "101"
				if strings.HasPrefix(one, "2") {
					host = "201"
				}
				out = append(out, map[string]string{"itemid": one, "hostid": host, "name": "x", "key_": "k", "value_type": "0"})
			}
			result = out
		case "event.get":
			result = []map[string]any{{"eventid": "9", "hosts": []map[string]string{{"hostid": "201"}}}}
		case "hostgroup.get":
			result = []map[string]any{
				{"groupid": "1", "name": "site1", "hosts": "1"},
				{"groupid": "2", "name": "site2", "hosts": "1"},
				{"groupid": "3", "name": "site1/Network", "hosts": "1"},
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
	s := &Server{st: st, zbx: zabbix.New(mock.URL, "test-token"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	local := &store.User{ID: 2, Role: "helpdesk", Sites: []string{"site1"}}
	admin := &store.User{ID: 1, Role: "admin"}

	call := func(u *store.User, h http.HandlerFunc, path, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if id != "" {
			req.SetPathValue("id", id)
		}
		req = req.WithContext(auth.WithUser(req.Context(), u))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	ok := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}

	// host list
	var got []hostView
	_ = json.Unmarshal(call(local, s.handleHosts, "/api/hosts", "").Body.Bytes(), &got)
	if len(got) != 2 || got[0].ID != "101" || got[1].ID != "102" {
		t.Fatalf("site1 user lists %+v", got)
	}
	_ = json.Unmarshal(call(admin, s.handleHosts, "/api/hosts", "").Body.Bytes(), &got)
	if len(got) != 3 {
		t.Fatalf("admin lists %d hosts", len(got))
	}
	// per-object guards
	for _, c := range []struct {
		h    http.HandlerFunc
		id   string
		want int
	}{
		{s.scopedHost(ok), "102", 200}, {s.scopedHost(ok), "201", 404}, {s.scopedHost(ok), "999", 404},
		{s.scopedItem(ok), "150", 200}, {s.scopedItem(ok), "250", 404},
		{s.scopedEvent(ok), "9", 404}, {s.scopedEvent(ok), "argus-unsupported-150", 200}, {s.scopedEvent(ok), "argus-unsupported-250", 404},
	} {
		if rec := call(local, c.h, "/x", c.id); rec.Code != c.want {
			t.Errorf("id %s: status %d, want %d", c.id, rec.Code, c.want)
		}
		if rec := call(admin, c.h, "/x", c.id); rec.Code != 200 {
			t.Errorf("admin, id %s: status %d", c.id, rec.Code)
		}
	}
	// groups
	var groups []groupView
	_ = json.Unmarshal(call(local, s.handleGroups, "/api/groups", "").Body.Bytes(), &groups)
	names := []string{}
	for _, g := range groups {
		names = append(names, g.Name)
	}
	if strings.Join(names, ",") != "site1,site1/Network" {
		t.Fatalf("site1 user sees groups %v", names)
	}
	// sparkline ids: another site's sensor is dropped before Zabbix is asked for its history
	ids, err := s.scopedItemIDs(t.Context(), scopeOf(local), []string{"150", "250", "151"})
	if err != nil || strings.Join(ids, ",") != "150,151" {
		t.Fatalf("scoped item ids = %v %v", ids, err)
	}
}
