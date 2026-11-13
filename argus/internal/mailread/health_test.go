// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package mailread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tenant's open service health issues, across pages, asked for unresolved; a refusal says which
// permission the app needs.
func TestServiceIssues(t *testing.T) {
	var srvURL string
	var filter string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tenant-2/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok2", "expires_in": 3600})
	})
	mux.HandleFunc("GET /v1.0/admin/serviceAnnouncement/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok2" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "Forbidden", "message": "Missing role permissions"}})
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": "TM2", "title": "Calls drop", "service": "Microsoft Teams", "classification": "advisory", "status": "serviceDegradation", "isResolved": false}}})
			return
		}
		filter = r.URL.Query().Get("$filter")
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
			{"id": "EX1", "title": "Users can't open mail", "service": "Exchange Online", "classification": "incident", "status": "serviceInterruption", "startDateTime": "2026-11-12T18:02:00Z", "isResolved": false},
			{"id": "EX0", "title": "Old", "service": "Exchange Online", "classification": "incident", "status": "serviceRestored", "isResolved": true}},
			"@odata.nextLink": srvURL + "/v1.0/admin/serviceAnnouncement/issues?page=2"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	oldG, oldL := graphBase, loginBase
	graphBase, loginBase = srv.URL+"/v1.0", srv.URL
	defer func() { graphBase, loginBase = oldG, oldL }()

	issues, err := ServiceIssues(context.Background(), "tenant-2", "app", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if filter != "isResolved eq false" || len(issues) != 2 || issues[0].ID != "EX1" || issues[0].Service != "Exchange Online" || issues[1].ID != "TM2" {
		t.Fatalf("filter %q, issues %+v", filter, issues)
	}

	tokens.Lock()
	tokens.m = map[string]graphToken{}
	tokens.Unlock()
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /tenant-2/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "other", "expires_in": 3600})
	})
	mux2.Handle("/", mux)
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	graphBase, loginBase = srv2.URL+"/v1.0", srv2.URL
	if _, err := ServiceIssues(context.Background(), "tenant-2", "app", "s3cret"); err == nil || !strings.Contains(err.Error(), "Missing role permissions (does the app have ServiceHealth.Read.All") {
		t.Fatalf("a refusal: %v", err)
	}
}
