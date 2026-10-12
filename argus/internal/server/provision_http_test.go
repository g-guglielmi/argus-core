// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// A device added with an HTTP check gets the port that was typed, or none: the template's port is
// blank and the collector takes the scheme's own (an HTTP device the scan saw on port 80 was checked
// on 443 while the template said 443).
func TestBuildMacrosHTTPPort(t *testing.T) {
	class, ok := provision.ClassByID("base")
	if !ok {
		t.Fatal("no base class")
	}
	macro := func(ms []zabbix.Macro, name string) (string, bool) {
		for _, m := range ms {
			if m.Macro == name {
				return m.Value, true
			}
		}
		return "", false
	}
	ms, err := buildMacros(createHostRequest{HTTP: true, HTTPScheme: "http"}, class)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := macro(ms, "{$HTTP.SCHEME}"); v != "http" {
		t.Errorf("scheme = %q", v)
	}
	if v, set := macro(ms, "{$HTTP.PORT}"); set {
		t.Errorf("no port typed, yet the host sets one: %q", v)
	}
	ms, err = buildMacros(createHostRequest{HTTP: true, HTTPScheme: "http", HTTPPort: " 8080 "}, class)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := macro(ms, "{$HTTP.PORT}"); v != "8080" {
		t.Errorf("typed port = %q", v)
	}
	for _, bad := range []string{"abc", "0", "70000", "80;rm"} {
		if _, err := buildMacros(createHostRequest{HTTP: true, HTTPScheme: "http", HTTPPort: bad}, class); err == nil {
			t.Errorf("port %q accepted", bad)
		}
	}
}
