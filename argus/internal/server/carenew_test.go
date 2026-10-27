// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/auth"
	"argus/internal/pki"
	"argus/internal/settings"
	"argus/internal/store"
	"argus/internal/zabbix"
)

func caPEM(t *testing.T, key *rsa.PrivateKey, serial int64, days int) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Monitoring Core CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(days) * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The renewed CA reaches the probes through the check-in, and Argus tells what is left to people.
func TestCheckinHandsTheRenewedCAToTheProbesAndTellsIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mgr, err := settings.New(ctx, st, zabbix.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	crt, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	_ = os.WriteFile(crt, caPEM(t, key, 1, 300), 0o644)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	ca, err := pki.Load(crt, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, mgr: mgr, ca: ca, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.digests.entries = map[string]digestEntry{}
	const raw = "probe-token"
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	checkin := func(fp string) string {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/probes/checkin", strings.NewReader(`{"version":"7.0.32-r7","scans":true,"ca":"`+fp+`"}`))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleProbeCheckin(w, r)
		if w.Code != 200 {
			t.Fatalf("check-in: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			CA string `json:"ca"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.CA
	}
	oldFP := ca.Fingerprint()
	colons := func(fp string) string { // as openssl prints it
		var b strings.Builder
		for i := 0; i < len(fp); i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(strings.ToUpper(fp[i : i+2]))
		}
		return b.String()
	}
	if got := checkin(colons(oldFP)); got != "" {
		t.Fatal("a probe on the CA Argus has was handed it again")
	}
	if got := checkin(""); got != "" {
		t.Fatal("a probe that reports no CA (an older image) was handed one")
	}
	if ag, _ := st.ProbeAgentByName(ctx, "proxy-site1"); ag.CAFingerprint != oldFP {
		t.Fatalf("stored %q, want %q (a report without it keeps the last one)", ag.CAFingerprint, oldFP)
	}

	now := time.Now()
	conds, events := s.caNotices(ctx, now)
	if len(events) != 0 {
		t.Fatalf("the first look at the CA is no renewal: %+v", events)
	}
	if len(conds) != 1 || !strings.HasPrefix(conds[0].key, "ca-ending:") {
		t.Fatalf("a CA ending within a year that nobody renewed is told: %+v", conds)
	}

	// The core VM renews it in place.
	_ = os.WriteFile(crt, caPEM(t, key, 2, 3650), 0o644)
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(crt, later, later)
	if changed, err := ca.Refresh(); !changed || err != nil {
		t.Fatalf("refresh: %v %v", changed, err)
	}
	if got := checkin(oldFP); got != string(ca.CertPEM()) {
		t.Fatalf("a probe on the old CA gets the renewed one, got %q", got)
	}
	conds, events = s.caNotices(ctx, now)
	if len(events) != 1 || events[0].key != "ca-renewed:"+ca.Fingerprint() || !strings.Contains(events[0].detail, "PCs that trust the old certificate") {
		t.Fatalf("the renewal is told once, with the PCs: %+v", events)
	}
	if len(conds) != 1 || !strings.HasPrefix(conds[0].key, "probe-ca:proxy-site1:") || conds[0].minAge != caProbeGrace ||
		!strings.Contains(conds[0].detail, "loses the core on") {
		t.Fatalf("a probe still on the old CA is told after a grace time: %+v", conds)
	}
	if _, events = s.caNotices(ctx, now); len(events) != 0 {
		t.Fatalf("the renewal is told once: %+v", events)
	}
	if got := checkin(ca.Fingerprint()); got != "" {
		t.Fatal("the probe took it: nothing more to hand out")
	}
	if conds, _ = s.caNotices(ctx, now); len(conds) != 0 {
		t.Fatalf("every probe on the renewed CA, far from its end: %+v", conds)
	}
}
