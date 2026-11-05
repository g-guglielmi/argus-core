// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"argus/internal/settings"
)

// A field kit is handed the field kit core host at check-in, every other probe the fleet's; with no
// field kit core host set, a kit gets the fleet's too.
func TestProbeKitCoreHost(t *testing.T) {
	g := newRebootRig(t)
	if err := g.s.mgr.Set(g.ctx, map[string]string{settings.KeyProbeCoreHost: "10.0.0.10"}); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) int {
		r := httptest.NewRequest("PUT", "/api/probes/"+name+"/kit", strings.NewReader(body))
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		g.s.handleSetProbeKit(w, r)
		return w.Code
	}
	if code := put("proxy-site1", `{"kit":true}`); code != 200 {
		t.Fatalf("kit on: %d", code)
	}
	if got := g.checkin()["core_host"]; got != "10.0.0.10" {
		t.Fatalf("a kit with no field kit core host got %v, want the fleet's", got)
	}
	if err := g.s.mgr.Set(g.ctx, map[string]string{settings.KeyProbeKitHost: "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	if got := g.checkin()["core_host"]; got != "198.51.100.7" {
		t.Fatalf("a kit got %v, want the field kit core host", got)
	}
	if code := put("proxy-site1", `{"kit":false}`); code != 200 {
		t.Fatalf("kit off: %d", code)
	}
	if got := g.checkin()["core_host"]; got != "10.0.0.10" {
		t.Fatalf("a probe like any other got %v, want the fleet's", got)
	}
	if code := put("proxy-site1", `{}`); code != 400 {
		t.Fatalf("no kit field: %d", code)
	}
	if code := put("proxy-nowhere", `{"kit":true}`); code != 404 {
		t.Fatalf("a probe that never checks in: %d", code)
	}
}
