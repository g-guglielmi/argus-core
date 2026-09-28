// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/store"
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
	s := &Server{st: st}
	ctx := context.Background()
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
	if w := open("/status/wrong-token", "10.0.0.5:4000", nil); w.Code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := open("/status", "10.0.0.5:4000", nil); w.Code != http.StatusNotFound {
		t.Fatalf("no cookie: %d", w.Code)
	}

	// Rotating the token kills the old link and the cookie that carries it.
	if err := st.RotateStatusPageToken(ctx, id, newStatusToken()); err != nil {
		t.Fatal(err)
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
	} {
		if got, ok := reachabilityReading(c.key, c.v); !ok || got != c.want {
			t.Errorf("reachabilityReading(%q, %q) = %q %v", c.key, c.v, got, ok)
		}
	}
	if _, ok := reachabilityReading("system.cpu.util", "0"); ok {
		t.Error("a CPU reading isn't a reachability")
	}
}
