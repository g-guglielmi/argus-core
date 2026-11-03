// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestProbeWindowValid(t *testing.T) {
	for _, tc := range []struct {
		w    rebootWindow
		want bool
	}{
		{rebootWindow{Mode: "auto", Weekday: everyDay, Hour: 3}, true},
		{rebootWindow{Mode: "auto", Weekday: 0, Hour: 2, Minute: 30}, true},
		{rebootWindow{Mode: "notify", Weekday: everyDay}, true},
		{rebootWindow{Mode: "auto", Weekday: -2, Hour: 3}, false},
		{rebootWindow{Mode: "auto", Weekday: everyDay, Hour: 24}, false},
		{rebootWindow{Mode: "weekly"}, false},
	} {
		if got := tc.w.valid(); got != tc.want {
			t.Errorf("%+v: valid = %v, want %v", tc.w, got, tc.want)
		}
	}
	if d := defaultProbeWindow(); d.Mode != "auto" || d.Weekday != everyDay || d.Hour != 3 || d.Minute != 0 {
		t.Errorf("default = %+v, want every day at 03:00 (what the probe VMs did before)", d)
	}
}

type rebootRig struct {
	t   *testing.T
	s   *Server
	st  *store.Store
	ctx context.Context
}

const rigToken = "probe-token"

func newRebootRig(t *testing.T) *rebootRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, mgr: mgr, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), diags: newDiagBoard()}
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(rigToken)); err != nil {
		t.Fatal(err)
	}
	return &rebootRig{t: t, s: s, st: st, ctx: ctx}
}

func (g *rebootRig) post(path, body string, h func(w *httptest.ResponseRecorder)) {
	g.t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+rigToken)
	w := httptest.NewRecorder()
	switch path {
	case "/api/probes/checkin":
		g.s.handleProbeCheckin(w, r)
	case "/api/probes/os-status":
		g.s.handleReportProbeOSStatus(w, r)
	default:
		g.t.Fatalf("no handler for %s", path)
	}
	h(w)
}

// checkin is the proxy container's minute report.
func (g *rebootRig) checkin() map[string]any {
	g.t.Helper()
	var out map[string]any
	g.post("/api/probes/checkin", `{"version":"7.0.32-r17","diags":true}`, func(w *httptest.ResponseRecorder) {
		if w.Code != 200 {
			g.t.Fatalf("check-in: %d %s", w.Code, w.Body.String())
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	})
	return out
}

func (g *rebootRig) report(body string) {
	g.t.Helper()
	g.post("/api/probes/os-status", body, func(w *httptest.ResponseRecorder) {
		if w.Code != 200 {
			g.t.Fatalf("os-status: %d %s", w.Code, w.Body.String())
		}
	})
}

func (g *rebootRig) request(kind string) (int, string) {
	g.t.Helper()
	r := httptest.NewRequest("POST", "/api/probes/proxy-site1/"+kind, nil)
	r.SetPathValue("name", "proxy-site1")
	w := httptest.NewRecorder()
	g.s.requestProbeHostAction(w, r, kind)
	return w.Code, w.Body.String()
}

// A Reboot now goes out at the probe's next check-in, once, and stays pending until the VM reports
// from after its boot.
func TestProbeRebootNowRoundTrip(t *testing.T) {
	g := newRebootRig(t)
	// A container probe gets no window: its OS is its Docker host's.
	if out := g.checkin(); out["reboot_window"] != nil {
		t.Fatalf("a container probe got a window: %v", out)
	}
	if code, body := g.request("reboot"); code != 409 || !strings.Contains(body, "not a probe VM") {
		t.Fatalf("reboot of a container probe: %d %s", code, body)
	}
	// An older reporter: the host tools don't take it yet.
	g.report(`{"sec_updates":0,"reboot_required":true,"os":"Debian 13.7"}`)
	if code, body := g.request("reboot"); code != 409 || !strings.Contains(body, "predate") {
		t.Fatalf("reboot with old host tools: %d %s", code, body)
	}
	g.report(`{"sec_updates":0,"reboot_required":true,"os":"Debian 13.7","reboot_now":true,"patch_now":true,"booted_at":1000}`)
	if code, body := g.request("patch"); code != 409 || !strings.Contains(body, "no security updates") {
		t.Fatalf("patch with nothing pending: %d %s", code, body)
	}
	out := g.checkin()
	win, _ := out["reboot_window"].(map[string]any)
	if win["mode"] != "auto" || win["weekday"] != float64(everyDay) || win["hour"] != float64(3) {
		t.Fatalf("window = %v, want the fleet default", out["reboot_window"])
	}
	if out["host_request"] != nil {
		t.Fatalf("a request before any was made: %v", out)
	}
	if code, body := g.request("reboot"); code != 202 {
		t.Fatalf("reboot: %d %s", code, body)
	}
	if code, body := g.request("reboot"); code != 409 || !strings.Contains(body, "already requested") {
		t.Fatalf("second reboot: %d %s", code, body)
	}
	req, _ := g.checkin()["host_request"].(map[string]any)
	if req["kind"] != "reboot" || req["at"] == nil || req["by"] != nil {
		t.Fatalf("handout = %v, want the reboot (and no requester)", req)
	}
	if out := g.checkin(); out["host_request"] != nil {
		t.Fatalf("the request went out twice: %v", out)
	}
	// The hourly report before the reboot doesn't settle it; the first one after boot does.
	g.report(`{"sec_updates":0,"reboot_required":true,"os":"Debian 13.7","reboot_now":true,"patch_now":true,"booted_at":1000}`)
	if h, _ := g.st.ProbeHostByName(g.ctx, "proxy-site1"); h.Req == "" {
		t.Fatal("a report from before the reboot settled the request")
	}
	g.report(`{"sec_updates":0,"reboot_required":false,"os":"Debian 13.7","reboot_now":true,"patch_now":true,"booted_at":` + itoa64(time.Now().Unix()+1) + `}`)
	if h, _ := g.st.ProbeHostByName(g.ctx, "proxy-site1"); h.Req != "" {
		t.Fatalf("the report after boot left the request: %s", h.Req)
	}
}

// Patch now settles on the patch result the VM reports after it.
func TestProbePatchNowSettlesOnItsResult(t *testing.T) {
	g := newRebootRig(t)
	g.checkin()
	g.report(`{"sec_updates":3,"reboot_required":false,"os":"Debian 13.7","reboot_now":true,"patch_now":true}`)
	if code, body := g.request("patch"); code != 202 {
		t.Fatalf("patch: %d %s", code, body)
	}
	if req, _ := g.checkin()["host_request"].(map[string]any); req["kind"] != "patch" {
		t.Fatalf("handout = %v", req)
	}
	at := itoa64(time.Now().Unix() + 1)
	g.report(`{"sec_updates":0,"reboot_required":true,"os":"Debian 13.7","reboot_now":true,"patch_now":true,"patch":{"state":"done","at":` + at + `,"why":"request","detail":"security updates applied"}}`)
	h, _ := g.st.ProbeHostByName(g.ctx, "proxy-site1")
	if h.Req != "" {
		t.Fatalf("the patch result left the request: %s", h.Req)
	}
	if v := probeHostViewOf(h, time.Now().Unix()); v.Patch == nil || v.Patch.State != "done" || !v.Now {
		t.Fatalf("view = %+v", v)
	}
}

// A probe's own window replaces the fleet's in its handout; {"fleet":true} puts it back.
func TestProbeOwnWindow(t *testing.T) {
	g := newRebootRig(t)
	g.checkin()
	g.report(`{"sec_updates":0,"reboot_required":false,"os":"Debian 13.7","reboot_now":true,"patch_now":true}`)
	put := func(body string) int {
		r := httptest.NewRequest("PUT", "/api/probes/proxy-site1/reboot-window", strings.NewReader(body))
		r.SetPathValue("name", "proxy-site1")
		w := httptest.NewRecorder()
		g.s.handleSetOwnProbeWindow(w, r)
		return w.Code
	}
	if code := put(`{"mode":"auto","weekday":0,"hour":2,"minute":0}`); code != 200 {
		t.Fatalf("own window: %d", code)
	}
	win, _ := g.checkin()["reboot_window"].(map[string]any)
	if win["weekday"] != float64(0) || win["hour"] != float64(2) {
		t.Fatalf("handout = %v, want Sunday 02:00", win)
	}
	if code := put(`{"mode":"auto","weekday":9,"hour":2}`); code != 400 {
		t.Fatalf("bad window: %d", code)
	}
	if code := put(`{"fleet":true}`); code != 200 {
		t.Fatalf("back to the fleet: %d", code)
	}
	if h, _ := g.st.ProbeHostByName(g.ctx, "proxy-site1"); h.Window != "" {
		t.Fatalf("window = %q, want the fleet's", h.Window)
	}
}

// The probe and its sidecar check in fast while the Updates page is open, and only then.
func TestProbesCheckInFastWhileTheUpdatesPageIsOpen(t *testing.T) {
	g := newRebootRig(t)
	if out := g.checkin(); out["next"] != nil {
		t.Fatalf("next = %v with nobody watching", out["next"])
	}
	g.s.handleWarmProbes(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/probes/warm", nil))
	if out := g.checkin(); out["next"] != float64(probeFastCheckin) {
		t.Fatalf("next = %v, want %d", out["next"], probeFastCheckin)
	}
}

// A custom probe pin must be a published release once the list is known.
func TestProbePinRefusal(t *testing.T) {
	rels := []string{"7.0.32-r17", "7.0.32-r16", "7.0.31-r9"}
	for _, tc := range []struct {
		target string
		rels   []string
		ok     bool
	}{
		{"latest", rels, true},
		{"7.0.31-r9", rels, true}, // an old one, past the picker's few
		{"7.0.31-r99", rels, false},
		{"7.0.31-r99", nil, true}, // GHCR not reached yet: the shape alone
	} {
		if got := probePinRefusal(tc.target, tc.rels) == ""; got != tc.ok {
			t.Errorf("%s with %v: ok = %v, want %v", tc.target, tc.rels, got, tc.ok)
		}
	}
}
