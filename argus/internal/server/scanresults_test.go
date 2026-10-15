// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"argus/internal/auth"
	"argus/internal/store"
)

// A probe reports on its own jobs only. Job ids are small integers, so a probe (one compromised
// VM at a customer site) must not be able to write the certificate the admin will pin onto
// another probe's job, nor tag a scan row with a controller its site was never handed: that
// reference decides whose API key is written onto the host when the row is adopted.
func TestScanResultsStayWithTheirProbe(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	s := &Server{st: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	tokens := map[string]string{"proxy-site1": "tok-site1", "proxy-site2": "tok-site2"}
	for name, tok := range tokens {
		if err := st.UpsertProbeCredential(ctx, name, auth.HashToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	// Site2's controller: handed to site2's probe only.
	ctlID, err := st.SaveUniFiController(ctx, store.UniFiController{Name: "site2 console", URL: "https://127.0.0.1:1", APIKey: "k2", Sites: []string{"site2"}, TLSMode: "ignore"})
	if err != nil {
		t.Fatal(err)
	}
	certJob, err := st.CreateDiscoveryJob(ctx, store.DiscoveryJob{ProxyName: "proxy-site2", Kind: "cert", CIDR: "https://127.0.0.1:1", RequestedBy: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	scanJob, err := st.CreateDiscoveryJob(ctx, store.DiscoveryJob{ProxyName: "proxy-site1", Kind: "scan", CIDR: "10.0.4.0/24", RequestedBy: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	post := func(tok string, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/probes/scan-results", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.handleScanResults(rec, req)
		return rec
	}
	cert := map[string]any{"fingerprint": "ab" + string(bytes.Repeat([]byte("cd"), 31)), "subject": "CN=impostor", "issuer": "CN=impostor", "not_after": "2030-01-01"}

	// Site1's probe reports a certificate for site2's job: nothing written.
	if rec := post("tok-site1", map[string]any{"job_id": certJob, "certificate": cert, "hosts": []any{}}); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign job accepted: %d %s", rec.Code, rec.Body.String())
	}
	if j, err := st.DiscoveryJobByID(ctx, certJob); err != nil || j.Certificate != "" || j.State == "done" {
		t.Fatalf("site2's job was touched by site1's probe: %+v %v", j, err)
	}
	// The owner's report lands.
	if rec := post("tok-site2", map[string]any{"job_id": certJob, "certificate": cert, "hosts": []any{}}); rec.Code != http.StatusOK {
		t.Fatalf("own job refused: %d %s", rec.Code, rec.Body.String())
	}
	if j, err := st.DiscoveryJobByID(ctx, certJob); err != nil || j.Certificate == "" {
		t.Fatalf("owner's certificate not kept: %+v %v", j, err)
	}

	// Site1's scan row names site2's controller: the reference is dropped, the row is kept.
	row := map[string]any{"ip": "10.0.4.7", "mac": "aa:bb:cc:dd:ee:01", "unifi": map[string]any{"type": "usw", "model": "USW-24", "mac": "aa:bb:cc:dd:ee:01"}, "unifi_ctl": ctlID}
	if rec := post("tok-site1", map[string]any{"job_id": scanJob, "hosts": []any{row}}); rec.Code != http.StatusOK {
		t.Fatalf("own scan refused: %d %s", rec.Code, rec.Body.String())
	}
	rows, err := st.DiscoveryResultsByJob(ctx, scanJob)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %v %v", rows, err)
	}
	if rows[0].ControllerID != 0 {
		t.Fatalf("a probe chose a controller outside its site: %d", rows[0].ControllerID)
	}
}

// Adoption writes a controller's URL and key onto the host only when the row's probe may be
// handed that controller, whatever reference the row carries (an older row, a tampered one).
func TestInjectUniFiMacrosKeepsControllersInTheirSites(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	s := &Server{st: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctlID, err := st.SaveUniFiController(ctx, store.UniFiController{Name: "site2 console", URL: "https://unifi.example.lan", APIKey: "k2", Sites: []string{"site2"}, TLSMode: "ignore"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateDiscoveryJob(ctx, store.DiscoveryJob{ProxyName: "proxy-site1", Kind: "scan", CIDR: "10.0.4.0/24", RequestedBy: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteDiscoveryJob(ctx, job, "proxy-site1", "", []store.DiscoveryResult{{IP: "10.0.4.7", MAC: "not a mac", UniFiJSON: `{"type":"usw","model":"USW-24"}`, ControllerID: ctlID}}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DiscoveryResultsByJob(ctx, job)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %v %v", rows, err)
	}
	req := &createHostRequest{DiscoveryResultID: rows[0].ID}
	s.injectUniFiMacros(ctx, req)
	if req.Macros["{$UNIFI.KEY}"] != "" || req.Macros["{$UNIFI.URL}"] != "" {
		t.Fatalf("site2's controller key injected on a site1 host: %v", req.Macros)
	}
	if _, set := req.Macros["{$UNIFI.MAC}"]; set {
		t.Fatalf("a MAC that isn't one was written: %v", req.Macros)
	}
}
