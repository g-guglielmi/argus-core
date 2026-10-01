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
	"sync"
	"testing"

	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestPushStatusAndMessage(t *testing.T) {
	for v, want := range map[string]bool{"": true, "ok": true, "UP": true, "success": true, "1": true, "fail": false, "down": false, "Error": false, "0": false} {
		if ok, valid := parsePushStatus(v); !valid || ok != want {
			t.Errorf("parsePushStatus(%q) = %v, %v", v, ok, valid)
		}
	}
	if _, valid := parsePushStatus("maybe"); valid {
		t.Error("an unknown status must be refused")
	}
	if got := cleanPushMsg("  Backup\tfailed:\r\n disk  full \x00"); got != "Backup failed: disk full" {
		t.Errorf("cleanPushMsg = %q", got)
	}
	if got := cleanPushMsg(strings.Repeat("x", 300)); len([]rune(got)) != maxPushMsg {
		t.Errorf("a long message is cut to %d runes, got %d", maxPushMsg, len([]rune(got)))
	}
}

func TestPushValidate(t *testing.T) {
	others := []store.PushSensor{{ID: 1, Name: "Nightly backup"}}
	ok := pushRequest{Name: " Weekly report ", LateSecs: 3600, MissedSecs: 7200}
	if req, msg := ok.validate(others, 0); msg != "" || req.Name != "Weekly report" {
		t.Fatalf("valid request refused: %q", msg)
	}
	for _, bad := range []pushRequest{
		{Name: "", LateSecs: 3600, MissedSecs: 7200},
		{Name: "nightly BACKUP", LateSecs: 3600, MissedSecs: 7200}, // the host has it already
		{Name: "a {#MACRO}", LateSecs: 3600, MissedSecs: 7200},
		{Name: "x", LateSecs: 60, MissedSecs: 7200},   // under 2 minutes
		{Name: "x", LateSecs: 7200, MissedSecs: 3600}, // missed before late
		{Name: "x", LateSecs: 3600, MissedSecs: 3600},
		{Name: strings.Repeat("n", maxPushName+1), LateSecs: 3600, MissedSecs: 7200},
	} {
		if _, msg := bad.validate(others, 0); msg == "" {
			t.Errorf("validate(%+v) passed", bad)
		}
	}
	if _, msg := (pushRequest{Name: "Nightly backup", LateSecs: 3600, MissedSecs: 7200}).validate(others, 1); msg != "" {
		t.Errorf("renaming a push sensor to its own name: %q", msg)
	}
}

// pushServer is a Server with a real store and the public push routes.
func pushServer(t *testing.T) (*Server, http.Handler) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{st: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/push/{token}", s.handlePushIngest)
	mux.HandleFunc("POST /api/push/{token}", s.handlePushIngest)
	mux.HandleFunc("GET /api/push/host/{id}", s.handlePushPoll)
	return s, mux
}

func do(h http.Handler, method, target, ct, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A job reports runs by GET, form POST or JSON POST; the host's template reads them back with its key.
func TestPushIngestAndPoll(t *testing.T) {
	s, h := pushServer(t)
	ctx := t.Context()
	id, err := s.st.CreatePushSensor(ctx, store.PushSensor{HostID: "10", Name: "Nightly backup", LateSecs: 90000, MissedSecs: 176400}, "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.st.CreatePushSensor(ctx, store.PushSensor{HostID: "10", Name: "Hourly sync", LateSecs: 4000, MissedSecs: 8000}, "tok-2")
	_, _ = s.st.CreatePushSensor(ctx, store.PushSensor{HostID: "20", Name: "Elsewhere", LateSecs: 4000, MissedSecs: 8000}, "tok-3")
	if err := s.st.SetPushHostKey(ctx, "10", "host-key"); err != nil {
		t.Fatal(err)
	}

	if w := do(h, "GET", "/api/push/tok-1?status=fail&msg=disk+full", "", "", nil); w.Code != 200 {
		t.Fatalf("GET report: %d %s", w.Code, w.Body)
	}
	p, _ := s.st.GetPushSensor(ctx, id)
	if p.LastOK || p.LastMsg != "disk full" || p.Runs != 1 || p.LastAt == 0 {
		t.Fatalf("after a failed run: %+v", p)
	}
	if w := do(h, "POST", "/api/push/tok-1", "application/x-www-form-urlencoded", url.Values{"status": {"ok"}, "msg": {"12 GB in 4m"}}.Encode(), nil); w.Code != 200 {
		t.Fatalf("form report: %d %s", w.Code, w.Body)
	}
	if p, _ = s.st.GetPushSensor(ctx, id); !p.LastOK || p.LastMsg != "12 GB in 4m" || p.Runs != 2 {
		t.Fatalf("after a form run: %+v", p)
	}
	if w := do(h, "POST", "/api/push/tok-1", "application/json", `{"status":"down","message":"Uptime Kuma style"}`, nil); w.Code != 200 {
		t.Fatalf("JSON report: %d %s", w.Code, w.Body)
	}
	if p, _ = s.st.GetPushSensor(ctx, id); p.LastOK || p.LastMsg != "Uptime Kuma style" {
		t.Fatalf("after a JSON run: %+v", p)
	}
	if w := do(h, "POST", "/api/push/tok-1", "", "", nil); w.Code != 200 { // a bare POST is a success
		t.Fatalf("bare POST: %d", w.Code)
	}
	if w := do(h, "GET", "/api/push/nope", "", "", nil); w.Code != 404 {
		t.Errorf("unknown token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/push/tok-1?status=sideways", "", "", nil); w.Code != 400 {
		t.Errorf("bad status: %d", w.Code)
	}

	// The template's read: only this host's push sensors, and only with its key.
	if w := do(h, "GET", "/api/push/host/10", "", "", map[string]string{"Authorization": "Bearer wrong"}); w.Code != 401 {
		t.Errorf("wrong key: %d", w.Code)
	}
	if w := do(h, "GET", "/api/push/host/20", "", "", map[string]string{"Authorization": "Bearer host-key"}); w.Code != 401 {
		t.Errorf("another host's key: %d", w.Code)
	}
	w := do(h, "GET", "/api/push/host/10", "", "", map[string]string{"Authorization": "Bearer host-key"})
	var got struct{ Sensors []pushPollEntry }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got.Sensors) != 2 {
		t.Fatalf("poll: %d %s", w.Code, w.Body)
	}
	if e := got.Sensors[0]; e.Name != "Nightly backup" || e.OK != 1 || e.Late != 90000 || e.Missed != 176400 || e.Message != "The last run reported success" || e.Age > 5 {
		t.Errorf("first sensor: %+v", e)
	}
	if e := got.Sensors[1]; e.Name != "Hourly sync" || e.OK != 1 || e.Message != "No run reported yet" {
		t.Errorf("a sensor that never ran: %+v", e)
	}
}

// Before its first run a push sensor ages from its creation, so a job that never starts is missed.
func TestPushPollAges(t *testing.T) {
	now := int64(1_000_000)
	got := pushPoll([]store.PushSensor{
		{ID: 1, Name: "never", CreatedAt: now - 500, LastOK: true},
		{ID: 2, Name: "failed", CreatedAt: now - 9000, LastAt: now - 60, LastOK: false},
		{ID: 3, Name: "future", CreatedAt: now, LastAt: now + 30, LastOK: true, LastMsg: "clock skew"},
	}, now)
	if got[0].Age != 500 || got[0].OK != 1 {
		t.Errorf("never ran: %+v", got[0])
	}
	if got[1].Age != 60 || got[1].OK != 0 || got[1].Message != "The last run reported a failure, without a message" {
		t.Errorf("failed: %+v", got[1])
	}
	if got[2].Age != 0 || got[2].Message != "clock skew" {
		t.Errorf("a run stamped ahead of now ages from 0: %+v", got[2])
	}
}

// Jobs report from curl or PowerShell with a form post: the push paths skip the browser checks, the
// management paths keep them.
func TestPushPathsSkipBrowserGuard(t *testing.T) {
	post := func(path string) *http.Request {
		r := httptest.NewRequest("POST", "http://10.0.0.10:8081"+path, strings.NewReader("status=ok"))
		r.Host = "10.0.0.10:8081"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	if ok, _ := hostGuardVerdict(post("/api/push/abc"), []string{"monitoring.example.com"}, "", false); !ok {
		t.Error("a job's form post must pass, by IP and with an allowed-hosts list")
	}
	if ok, _ := hostGuardVerdict(post("/api/push-sensors/1"), nil, "", false); ok {
		t.Error("a form post to the management API must be refused")
	}
}

// The push sensors read as their own group, the last run as OK / Failed with the job's message.
func TestPushClassify(t *testing.T) {
	cat, label, inst, ch, ok := classifyItem("argus.push.ok[7]", "Nightly backup last run")
	if !ok || cat != "Push" || inst != "Nightly backup" || ch != "Last run" || label != "Nightly backup last run" {
		t.Errorf("ok item: %q %q %q %q %v", cat, label, inst, ch, ok)
	}
	if _, _, inst, ch, _ = classifyItem("argus.push.age[7]", "Nightly backup since last run"); inst != "Nightly backup" || ch != "Since last run" {
		t.Errorf("age item: %q %q", inst, ch)
	}
	if _, _, _, _, ok = classifyItem("argus.push.message[7]", "Nightly backup message"); ok {
		t.Error("the message is the reason, not a sensor")
	}
	if _, _, _, _, ok = classifyItem("argus.push.raw", "Push sensors raw data"); ok {
		t.Error("the master item is not a sensor")
	}
	if r, _ := runningReading("argus.push.ok[7]", "0"); r != "Failed" {
		t.Errorf("a failed run reads %q", r)
	}
	if r, _ := runningReading("linux.ssh.unit.active[nginx]", "1"); r != "Running" {
		t.Errorf("a unit still reads %q", r)
	}
	if reasonKeyFor("argus.push.ok[7]") != "argus.push.message[7]" || !isUpDownKey("argus.push.ok[7]") {
		t.Error("the last run's reason is its message, and it has an uptime")
	}
}

// An HTTP check reads as one group per URL under Web, its error as the reason.
func TestHTTPClassify(t *testing.T) {
	for key, want := range map[string][2]string{
		"http.url.up[ab12]":   {"portal.example.com/app reachable", "Reachable"},
		"http.url.time[ab12]": {"portal.example.com/app response time", "Response time"},
		"http.url.cert[ab12]": {"portal.example.com/app certificate expires in", "Certificate"},
	} {
		cat, label, inst, ch, ok := classifyItem(key, want[0])
		if !ok || cat != "Web" || inst != "portal.example.com/app" || ch != want[1] || label != want[0] {
			t.Errorf("%s: %q %q %q %q %v", key, cat, label, inst, ch, ok)
		}
	}
	if _, _, _, _, ok := classifyItem("http.url.error[ab12]", "portal.example.com/app error"); ok {
		t.Error("the error item is the reason, not a sensor")
	}
	if reasonKeyFor("http.url.up[ab12]") != "http.url.error[ab12]" || !isUpDownKey("http.url.up[ab12]") {
		t.Error("a URL's reason and uptime")
	}
}

// zbxRecorder is a Zabbix API mock for the push template steps: it keeps the host's macros and
// whether the template is linked, and records the methods called.
type zbxRecorder struct {
	mu      sync.Mutex
	linked  bool
	macros  map[string]zabbix.HostMacro
	nextID  int
	methods []string
}

func (z *zbxRecorder) serve(w http.ResponseWriter, r *http.Request) {
	z.mu.Lock()
	defer z.mu.Unlock()
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     any             `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	z.methods = append(z.methods, req.Method)
	var p map[string]any
	_ = json.Unmarshal(req.Params, &p)
	var result any = []any{}
	switch req.Method {
	case "host.get":
		var tpl []map[string]string
		if z.linked {
			tpl = append(tpl, map[string]string{"host": "Argus Push"})
		}
		result = []map[string]any{{"hostid": "10", "parentTemplates": tpl}}
	case "template.get":
		result = []map[string]string{{"templateid": "500", "host": "Argus Push", "name": "Argus Push"}}
	case "host.massadd":
		z.linked = true
	case "host.update":
		if _, clear := p["templates_clear"]; clear {
			z.linked = false
		}
	case "usermacro.get":
		var out []map[string]string
		for _, m := range z.macros {
			v := m.Value
			if m.Type == 1 {
				v = "" // a secret never reads back
			}
			out = append(out, map[string]string{"hostmacroid": m.MacroID, "macro": m.Macro, "value": v, "type": map[int]string{0: "0", 1: "1"}[m.Type]})
		}
		result = out
	case "usermacro.create":
		z.nextID++
		id := string(rune('a' + z.nextID))
		typ := 0
		if t, _ := p["type"].(float64); t == 1 {
			typ = 1
		}
		z.macros[p["macro"].(string)] = zabbix.HostMacro{MacroID: id, Macro: p["macro"].(string), Value: p["value"].(string), Type: typ}
	case "usermacro.update":
		for k, m := range z.macros {
			if m.MacroID == p["hostmacroid"] {
				m.Value = p["value"].(string)
				z.macros[k] = m
			}
		}
	case "usermacro.delete":
		var ids []string
		_ = json.Unmarshal(req.Params, &ids)
		for _, id := range ids {
			for k, m := range z.macros {
				if m.MacroID == id {
					delete(z.macros, k)
				}
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
}

func (z *zbxRecorder) count(method string) int {
	z.mu.Lock()
	defer z.mu.Unlock()
	n := 0
	for _, m := range z.methods {
		if m == method {
			n++
		}
	}
	return n
}

// The first push sensor links the template and gives the host its URL and key; saving again keeps the
// key; the last one gone takes it all back off.
func TestEnsurePushHost(t *testing.T) {
	z := &zbxRecorder{macros: map[string]zabbix.HostMacro{}}
	mock := httptest.NewServer(http.HandlerFunc(z.serve))
	t.Cleanup(mock.Close)
	s, _ := pushServer(t)
	s.zbx = zabbix.New(mock.URL, "test-token")
	r := httptest.NewRequest("POST", "https://10.0.0.10/api/hosts/10/push", nil)
	ctx := t.Context()
	mgr, err := settings.New(ctx, s.st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s.mgr = mgr
	if msg := s.pushHostProblem(ctx, r, "10"); !strings.Contains(msg, "Public URL") {
		t.Fatalf("without an address the probes can reach: %q", msg)
	}
	if err := mgr.Set(ctx, map[string]string{settings.KeyPublicURL: "https://monitoring.example.com"}); err != nil {
		t.Fatal(err)
	}
	if msg := s.pushHostProblem(ctx, r, "10"); msg != "" {
		t.Fatalf("pushHostProblem: %q", msg)
	}
	if err := s.ensurePushHost(ctx, r, "10"); err != nil {
		t.Fatal(err)
	}
	if !z.linked || z.macros[pushMacroURL].Value != "https://monitoring.example.com/api/push/host/10" || z.macros[pushMacroKey].Type != 1 {
		t.Fatalf("after the first: linked %v macros %+v", z.linked, z.macros)
	}
	key := z.macros[pushMacroKey].Value
	if h, err := s.st.PushHostKeyHash(ctx, "10"); err != nil || h != store.HashStatusToken(key) {
		t.Fatalf("stored key hash %q, %v", h, err)
	}
	if err := s.ensurePushHost(ctx, r, "10"); err != nil {
		t.Fatal(err)
	}
	if z.macros[pushMacroKey].Value != key || z.count("host.massadd") != 1 || z.count("usermacro.update") != 0 {
		t.Errorf("saving again changed something: %v", z.methods)
	}
	if err := s.dropPushHost(ctx, "10"); err != nil {
		t.Fatal(err)
	}
	if z.linked || len(z.macros) != 0 {
		t.Errorf("after the last: linked %v macros %+v", z.linked, z.macros)
	}
	if _, err := s.st.PushHostKeyHash(ctx, "10"); err == nil {
		t.Error("the key is forgotten")
	}
}


