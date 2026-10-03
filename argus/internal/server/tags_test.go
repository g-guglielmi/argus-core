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

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestTagsMatch(t *testing.T) {
	if !tagsMatch(nil, nil) || !tagsMatch(nil, []string{"x"}) {
		t.Fatal("a channel with no tags serves every host")
	}
	if tagsMatch([]string{"critical"}, nil) || tagsMatch([]string{"critical"}, []string{"poe"}) {
		t.Fatal("a tagged channel served a host without its tag")
	}
	if !tagsMatch([]string{"critical", "backup"}, []string{"poe", "backup"}) {
		t.Fatal("one shared tag is enough")
	}
	d := notifyDest{alerts: true, minSev: 2, tags: []string{"critical"}}
	if d.serves(hostRoute{groups: []string{"site1"}, tags: []string{"poe"}}, 4) || !d.serves(hostRoute{groups: []string{"site1"}, tags: []string{"critical"}}, 4) {
		t.Fatal("serves ignores the tags")
	}
}

// fakeZabbix answers host.get with the given hosts and every other call with an empty list.
func fakeZabbix(t *testing.T, hosts []map[string]any) *zabbix.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = []any{}
		if req.Method == "host.get" {
			result = hosts
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
	}))
	t.Cleanup(srv.Close)
	return zabbix.New(srv.URL, "test-token")
}

// A bulk tag change adds and removes on every host it may, reports the ones it can't, and logs once.
// A probe's tags reach its hosts in the host index.
func TestBulkTagsAndProbeTags(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	for _, n := range []string{"critical", "poe"} {
		if err := st.CreateTag(ctx, store.Tag{Name: n, Color: "#e5484d"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetHostTags(ctx, "21", []string{"poe"})
	_ = st.SetProbeTags(ctx, "10", []string{"critical"})
	hosts := []map[string]any{
		{"hostid": "21", "name": "ap-lobby", "proxyid": "10", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1/Network"}}},
		{"hostid": "22", "name": "ap-office", "proxyid": "0", "hostgroups": []map[string]string{{"groupid": "1", "name": "site1/Network"}}},
	}
	s := &Server{st: st, zbx: fakeZabbix(t, hosts), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/bulk/hosts", s.handleBulkHosts)
	s.mux = mux
	h := s.changeLog(mux)

	body := `{"action":"tags","host_ids":["21","22","99"],"add":["critical"],"remove":["poe"]}`
	req := httptest.NewRequest("POST", "/api/bulk/hosts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: 1, Email: "admin@example.com", Role: "admin"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var res bulkResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || rr.Code != 200 {
		t.Fatalf("answer %d %s", rr.Code, rr.Body.String())
	}
	if res.Done != 2 || len(res.Failed) != 1 || res.Failed[0].ID != "99" {
		t.Fatalf("result: %+v", res)
	}
	own, _ := st.HostTags(ctx)
	if got := own["21"]; len(got) != 1 || got[0] != "critical" {
		t.Fatalf("host 21 own tags: %v", got)
	}
	cs := waitChanges(t, st, 1)
	if len(cs) != 1 || cs[0].Action != "Changed the tags of 2 hosts" || cs[0].Object != "ap-lobby, ap-office" || !strings.Contains(cs[0].Detail, "added critical; removed poe") || !strings.Contains(cs[0].Detail, "1 host failed") {
		t.Fatalf("change entry: %+v", cs)
	}

	idx, err := s.hostTagIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// ap-lobby has "critical" itself now, so its probe's copy isn't repeated; ap-office (on the server)
	// has only its own.
	if got := idx["21"]; len(got) != 1 || got[0].Name != "critical" || got[0].From != "" {
		t.Fatalf("ap-lobby tags: %+v", got)
	}
	_ = st.SetHostTags(ctx, "21", nil)
	idx, _ = s.hostTagIndex(ctx)
	if got := idx["21"]; len(got) != 1 || got[0].From == "" {
		t.Fatalf("ap-lobby should carry its probe's tag: %+v", got)
	}
}
