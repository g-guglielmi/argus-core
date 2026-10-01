// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func TestNormalizeCIDRs(t *testing.T) {
	got, msg := normalizeCIDRs("10.0.0.0/24, 192.168.1.5 ;fd00::/8\n10.1.2.3/16")
	if msg != "" || got != "10.0.0.0/24, 192.168.1.5/32, fd00::/8, 10.1.0.0/16" {
		t.Fatalf("normalize: %q %q", got, msg)
	}
	if _, msg := normalizeCIDRs("10.0.0.0/24, office"); msg == "" {
		t.Fatal("a non-network was accepted")
	}
	if got, msg := normalizeCIDRs("  "); got != "" || msg != "" {
		t.Fatalf("empty: %q %q", got, msg)
	}
}

func TestCIDRAllows(t *testing.T) {
	if !cidrAllows("", "203.0.113.9") {
		t.Fatal("no list must allow everyone")
	}
	list := "10.0.0.0/24, 192.168.1.5/32"
	for ip, want := range map[string]bool{"10.0.0.77": true, "192.168.1.5": true, "192.168.1.6": false, "::ffff:10.0.0.9": true, "garbage": false} {
		if got := cidrAllows(list, ip); got != want {
			t.Errorf("cidrAllows(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestStatusCovers(t *testing.T) {
	if !statusCovers(nil, "site1/Network") || !statusCovers([]string{"site1"}, "site1/Network") || statusCovers([]string{"site2"}, "site1") {
		t.Fatal("statusCovers")
	}
}

// Opening the link trades the token for a cookie and a clean address; the page then opens from the
// cookie alone. A wrong token, an expired page or another network get a plain refusal.
func TestStatusLinkFlow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, mgr: mgr}
	token := newStatusToken()
	id, err := st.CreateStatusPage(ctx, store.StatusPage{Name: "Rack", AllowCIDRs: "10.0.0.0/24"}, token)
	if err != nil {
		t.Fatal(err)
	}

	open := func(path, remote string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = remote
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if strings.HasPrefix(path, "/status/") {
			r.SetPathValue("token", strings.TrimPrefix(path, "/status/"))
		}
		w := httptest.NewRecorder()
		if path == "/status" {
			s.handleStatusPage(w, r)
		} else {
			s.handleStatusLink(w, r)
		}
		return w
	}

	w := open("/status/"+token, "10.0.0.5:4000", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/status" {
		t.Fatalf("link: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("headers: %v", w.Header())
	}
	var ck *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == statusCookie {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || ck.Path != "/status" || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", ck)
	}
	if w := open("/status", "10.0.0.5:4000", ck); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Argus status</title>") {
		t.Fatalf("page: %d", w.Code)
	}
	if w := open("/status", "10.9.9.9:4000", ck); w.Code != http.StatusForbidden {
		t.Fatalf("other network: %d", w.Code)
	}
	// Behind a trusted proxy the allowlist checks the real client, not the proxy.
	if err := mgr.Set(ctx, map[string]string{settings.KeyTrustProxy: "192.168.9.1"}); err != nil {
		t.Fatal(err)
	}
	viaProxy := func(xff string) int {
		r := httptest.NewRequest("GET", "/status", nil)
		r.RemoteAddr = "192.168.9.1:4000"
		r.Header.Set("X-Forwarded-For", xff)
		r.AddCookie(ck)
		w := httptest.NewRecorder()
		s.handleStatusPage(w, r)
		return w.Code
	}
	if code := viaProxy("10.0.0.5"); code != http.StatusOK {
		t.Fatalf("LAN client via proxy: %d", code)
	}
	if code := viaProxy("10.0.0.5, 203.0.113.9"); code != http.StatusForbidden {
		t.Fatalf("outside client forging a LAN address via proxy: %d", code)
	}
	_ = mgr.Set(ctx, map[string]string{settings.KeyTrustProxy: ""})

	if w := open("/status/wrong-token", "10.0.0.5:4000", nil); w.Code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := open("/status", "10.0.0.5:4000", nil); w.Code != http.StatusNotFound {
		t.Fatalf("no cookie: %d", w.Code)
	}

	// The link can be copied again: the token is kept (encrypted), and the page says so.
	if got, err := st.StatusPageToken(ctx, id); err != nil || got != token {
		t.Fatalf("stored token: %q %v", got, err)
	}
	if pg, _ := st.GetStatusPage(ctx, id); !pg.HasLink {
		t.Fatal("HasLink false for a page with a kept token")
	}

	// Rotating the token kills the old link and the cookie that carries it.
	tok3 := newStatusToken()
	if err := st.RotateStatusPageToken(ctx, id, tok3); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.StatusPageToken(ctx, id); got != tok3 {
		t.Fatalf("rotated token not kept: %q", got)
	}
	if w := open("/status", "10.0.0.5:4000", ck); w.Code != http.StatusNotFound {
		t.Fatalf("rotated: %d", w.Code)
	}

	// An expired page is gone.
	tok2 := newStatusToken()
	id2, _ := st.CreateStatusPage(ctx, store.StatusPage{Name: "Old"}, tok2)
	p, _ := st.GetStatusPage(ctx, id2)
	p.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	_ = st.UpdateStatusPage(ctx, *p)
	if w := open("/status/"+tok2, "10.0.0.5:4000", nil); w.Code != http.StatusGone {
		t.Fatalf("expired: %d", w.Code)
	}
}

func TestReachabilityReading(t *testing.T) {
	for _, c := range []struct{ key, v, want string }{
		{"icmpping", "0", "No reply to ping"}, {"icmpping", "1", "Replying to ping"},
		{"nut.reachable", "0", "Not reachable"}, {"xcp.reachable", "1", "Reachable"},
		{"net.tcp.service[https,,443]", "0", "Not reachable"},
	} {
		if got, ok := reachabilityReading(c.key, c.v); !ok || got != c.want {
			t.Errorf("reachabilityReading(%q, %q) = %q %v", c.key, c.v, got, ok)
		}
	}
	if _, ok := reachabilityReading("system.cpu.util", "0"); ok {
		t.Error("a CPU reading isn't a reachability")
	}
}

// A dead device is one row on a status page: the sensors its down master holds fold into the
// master's row. One whose master isn't listed keeps its row.
func TestFoldHeld(t *testing.T) {
	ping := &heldRef{HostID: "10", ItemID: "100", Name: "Reachable (ICMP)"}
	gone := &heldRef{HostID: "70", ItemID: "700", Name: "Uptime"} // a master not on the page
	rows := []sensorRow{
		{ItemID: "100", State: "error"},
		{ItemID: "103", State: "error", HeldBy: ping},
		{ItemID: "104", State: "warning", HeldBy: ping},
		{ItemID: "105", State: "acked", HeldBy: ping},
		{ItemID: "200", State: "error"},
		{ItemID: "300", State: "error", HeldBy: gone},
	}
	kept, holds := foldHeld(rows)
	var ids []string
	for _, r := range kept {
		ids = append(ids, r.ItemID)
	}
	if strings.Join(ids, ",") != "100,200,300" {
		t.Fatalf("kept %v, want the master, the other host and the one whose master is off the page", ids)
	}
	if holds["100"] != 3 || len(holds) != 1 {
		t.Fatalf("holds %v, want 3 on the ping", holds)
	}
}

// A note keeps its paragraphs, loses control characters and runs of blank lines, and shows until its end.
func TestStatusNote(t *testing.T) {
	if got := cleanNote("  Internet at site3 is down.\r\n\r\n\r\n\tISP ticket open \x07\n"); got != "Internet at site3 is down.\n\nISP ticket open" {
		t.Errorf("cleanNote = %q", got)
	}
	n := store.StatusNote{Text: "x", Until: 1000}
	if !n.Showing(999) || n.Showing(1000) || !(store.StatusNote{Text: "x"}).Showing(1<<40) || (store.StatusNote{}).Showing(0) {
		t.Error("Showing")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{st: st}
	id, _ := st.CreateStatusPage(t.Context(), store.StatusPage{Name: "Rack"}, newStatusToken())
	put := func(body string) int {
		r := httptest.NewRequest("PUT", "/api/status-pages/1/note", strings.NewReader(body))
		r.SetPathValue("id", strconv.FormatInt(id, 10))
		w := httptest.NewRecorder()
		s.handleSetStatusNote(w, r)
		return w.Code
	}
	for _, bad := range []string{`{"text":"  ","style":"info"}`, `{"text":"x","style":"loud"}`, `{"text":"x","style":"info","until":5}`,
		`{"text":"` + strings.Repeat("y", maxNoteRunes+1) + `","style":"info"}`} {
		if code := put(bad); code != http.StatusBadRequest {
			t.Errorf("put %.60s: %d", bad, code)
		}
	}
	if code := put(`{"text":"ISP ticket open","style":"problem","until":` + strconv.FormatInt(time.Now().Unix()+3600, 10) + `}`); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	p, _ := st.GetStatusPage(t.Context(), id)
	if p.Note.Text != "ISP ticket open" || p.Note.Style != "problem" || p.Note.At == 0 {
		t.Errorf("stored note: %+v", p.Note)
	}
	_ = st.SetStatusNote(t.Context(), id, store.StatusNote{})
	if p, _ = st.GetStatusPage(t.Context(), id); p.Note.Text != "" || toStatusPageView(*p).Note != nil {
		t.Errorf("removed note: %+v", p.Note)
	}
}

// The page lists the windows touching its hosts: the ones in progress, then up to three coming within
// a week, with the hosts they cover.
func TestStatusMaintenance(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, loc) // a Thursday, 01:00
	hosts := map[string][]string{"10": {"site1"}, "11": {"site1/Network"}, "20": {"site2"}}
	names := map[string]string{"10": "nas1", "11": "sw1", "20": "srv2"}
	windows := []store.MaintenanceWindow{
		{ID: 1, Name: "Nightly backups", Sites: []string{"site1"}, Kind: "daily", Minute: 30, DurationMin: 120, Enabled: true},                    // 00:30-02:30, now
		{ID: 2, Name: "Patch night", HostIDs: []string{"20"}, Kind: "weekly", Weekdays: 1 << 6, Minute: 22 * 60, DurationMin: 240, Enabled: true}, // Saturday 22:00
		{ID: 3, Name: "Elsewhere", Sites: []string{"site9"}, Kind: "daily", Minute: 0, DurationMin: 600, Enabled: true},                           // no host on the page
		{ID: 4, Name: "Off", Sites: []string{"site1"}, Kind: "daily", Minute: 0, DurationMin: 600, Enabled: false},
		{ID: 5, Name: "Next month", Sites: []string{"site2"}, Kind: "once", StartAt: now.Add(20 * 24 * time.Hour).Unix(), DurationMin: 60, Enabled: true},
	}
	got := statusMaintenance(windows, hosts, names, loc, now)
	if len(got) != 3 {
		t.Fatalf("got %d windows: %+v", len(got), got)
	}
	if a := got[0]; !a.Active || a.Name != "Nightly backups" || a.Hosts != 2 || strings.Join(a.Names, ",") != "nas1,sw1" ||
		a.End != time.Date(2026, 10, 1, 2, 30, 0, 0, loc).Unix() {
		t.Errorf("in progress: %+v", a)
	}
	if n := got[1]; n.Active || n.Name != "Nightly backups" || n.Start != time.Date(2026, 10, 2, 0, 30, 0, 0, loc).Unix() {
		t.Errorf("its next run: %+v", n)
	}
	if n := got[2]; n.Name != "Patch night" || n.Hosts != 1 || n.Start != time.Date(2026, 10, 3, 22, 0, 0, 0, loc).Unix() {
		t.Errorf("coming up: %+v", n)
	}
}
