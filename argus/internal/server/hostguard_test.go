// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/http/httptest"
	"testing"
)

func TestHostGuardVerdict(t *testing.T) {
	list := []string{"10.0.0.10"}
	pub := "https://monitoring.example.com"
	cases := []struct {
		name, method, host, path, origin, xfh string
		list                                  []string
		trust                                 bool
		ok                                    bool
	}{
		{"off when the list is empty", "GET", "evil.example.net", "/api/hosts", "", "", nil, false, true},
		{"listed IP", "GET", "10.0.0.10:8081", "/api/hosts", "", "", list, false, true},
		{"public URL host always allowed", "GET", "monitoring.example.com", "/api/hosts", "", "", list, false, true},
		{"loopback always allowed", "GET", "127.0.0.1:8081", "/api/me", "", "", list, false, true},
		{"localhost always allowed", "GET", "localhost:8081", "/api/me", "", "", list, false, true},
		{"rebinding host refused", "GET", "evil.example.net", "/api/me", "", "", list, false, false},
		{"login refused too", "POST", "evil.example.net", "/api/login", "", "", list, false, false},
		{"SPA assets are not checked", "GET", "evil.example.net", "/assets/index.js", "", "", list, false, true},
		{"probe check-in never checked", "POST", "10.9.9.9", "/api/probes/checkin", "", "", list, false, true},
		{"enrollment never checked", "POST", "10.9.9.9", "/api/enroll", "", "", list, false, true},
		{"same-origin change", "POST", "10.0.0.10:8081", "/api/settings", "http://10.0.0.10:8081", "", list, false, true},
		{"sibling-origin change refused", "POST", "monitoring.example.com", "/api/settings", "https://other.example.com", "", list, false, false},
		{"null origin refused", "DELETE", "10.0.0.10", "/api/hosts/1", "null", "", list, false, false},
		{"cross-origin GET is Lax's job", "GET", "10.0.0.10", "/api/hosts", "https://other.example.com", "", list, false, true},
		{"forwarded host honoured behind a trusted proxy", "GET", "argus:8081", "/api/me", "", "monitoring.example.com", list, true, true},
		{"forwarded host ignored without trust", "GET", "argus:8081", "/api/me", "", "monitoring.example.com", list, false, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://"+c.host+c.path, nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.xfh != "" {
			r.Header.Set("X-Forwarded-Host", c.xfh)
		}
		if ok, msg := hostGuardVerdict(r, c.list, pub, c.trust); ok != c.ok {
			t.Errorf("%s: ok=%v (%s), want %v", c.name, ok, msg, c.ok)
		}
	}
}

func TestAllowedHostsLockout(t *testing.T) {
	r := httptest.NewRequest("PATCH", "http://10.0.0.10:8081/api/settings", nil)
	pub := "https://monitoring.example.com"
	if msg := allowedHostsLockout(r, map[string]string{"allowed_hosts": "monitoring.example.com"}, nil, pub, false); msg == "" {
		t.Fatal("a list without the address in use must be refused")
	}
	if msg := allowedHostsLockout(r, map[string]string{"allowed_hosts": "monitoring.example.com, 10.0.0.10"}, nil, pub, false); msg != "" {
		t.Fatalf("list including the address in use refused: %s", msg)
	}
	if msg := allowedHostsLockout(r, map[string]string{"allowed_hosts": "*"}, []string{"10.0.0.10"}, pub, false); msg != "" {
		t.Fatalf("turning the check off refused: %s", msg)
	}
	// Changing the Public URL while the list is on must not strand an admin who relied on it.
	pr := httptest.NewRequest("PATCH", "https://monitoring.example.com/api/settings", nil)
	if msg := allowedHostsLockout(pr, map[string]string{"public_url": "https://argus.example.com"}, []string{"10.0.0.10"}, pub, false); msg == "" {
		t.Fatal("moving the Public URL away from the address in use must be refused")
	}
	if msg := allowedHostsLockout(pr, map[string]string{"timezone": "Europe/Rome"}, []string{"10.0.0.10"}, pub, false); msg != "" {
		t.Fatalf("unrelated change refused: %s", msg)
	}
}
