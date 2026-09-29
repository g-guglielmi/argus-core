// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"127.0.0.1:8081": "http://127.0.0.1:8081/healthz",
		"[::]:8080":      "http://127.0.0.1:8080/healthz",
		"[::1]:8080":     "http://[::1]:8080/healthz",
		"10.0.0.10:8080": "http://10.0.0.10:8080/healthz",
		"garbage":        "http://127.0.0.1:8080/healthz",
	}
	for in, want := range cases {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer ok.Close()
	_, port, _ := net.SplitHostPort(ok.Listener.Addr().String())
	t.Setenv("ARGUS_LISTEN", ":"+port)
	if code := runHealthcheck(); code != 0 {
		t.Fatalf("a server answering ok: exit %d", code)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	_, port, _ = net.SplitHostPort(bad.Listener.Addr().String())
	t.Setenv("ARGUS_LISTEN", ":"+port)
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("a server answering 503: exit %d", code)
	}

	bad.Close() // nothing listening
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("nothing listening: exit %d", code)
	}
}
