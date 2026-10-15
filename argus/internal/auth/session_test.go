// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Over HTTPS only the __Host- cookie is a session: one under the bare name, which a sibling host
// or a plain-HTTP page can set, is ignored. Over plain HTTP the bare name is the only one there is.
func TestSessionCookieReadsOnlyTheNameInUse(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "planted"})
	if got := SessionCookie(r, true); got != "" {
		t.Fatalf("secure mode read the bare cookie: %q", got)
	}
	if got := SessionCookie(r, false); got != "planted" {
		t.Fatalf("plain mode ignored the bare cookie: %q", got)
	}
	r.AddCookie(&http.Cookie{Name: SecureCookieName, Value: "real"})
	if got := SessionCookie(r, true); got != "real" {
		t.Fatalf("secure mode: %q", got)
	}
	if got := SessionCookie(r, false); got != "planted" {
		t.Fatalf("plain mode must not read the __Host- name: %q", got)
	}
}
