// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/http"
	"os"
	"regexp"
	"testing"
)

// TestRoutesRegister registers every route server.go registers on a fresh ServeMux: two patterns
// that overlap without one being more specific panic there, and in the real server that is a
// crash loop at startup (DELETE /api/probes/{name}/kit-hosts against DELETE
// /api/probes/tokens/{id}, 2026-11-05). It also checks that the change log's keys are routes.
func TestRoutesRegister(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) < 100 {
		t.Fatalf("found only %d routes in server.go: the pattern no longer matches how routes are registered", len(found))
	}
	mux := http.NewServeMux()
	routes := map[string]bool{}
	for _, m := range found {
		pattern := m[1]
		routes[pattern] = true
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("route %q: %v", pattern, r)
				}
			}()
			mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		}()
	}
	for key := range changeRoutes {
		if !routes[key] {
			t.Errorf("the change log describes %q, which isn't a registered route", key)
		}
	}
}
