// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
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

func loadTestCA(t *testing.T, dir string) *pki.CA {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	crt, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	_ = os.WriteFile(crt, caPEM(t, key, 1, 3650), 0o644)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	ca, err := pki.Load(crt, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func csrFor(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "proxy"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	// As the probe sends it: the PEM without its last newline.
	return strings.TrimRight(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), "\n")
}

func signText(t *testing.T, key *rsa.PrivateKey, text string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(text))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// A probe renews its own certificate with its token AND proof of its current key; a token alone, a
// certificate for another probe or from another CA gets nothing.
func TestProbeRenewsItsCertificateWithProofOfItsKey(t *testing.T) {
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
	ca := loadTestCA(t, dir)
	s := &Server{st: st, mgr: mgr, ca: ca, loginLimiter: mgr.Limiter(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const raw = "probe-token"
	if err := st.UpsertProbeCredential(ctx, "proxy-site1", auth.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	curKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	curPEM, err := ca.SignCSR([]byte(csrFor(t, curKey)), "proxy-site1", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	renew := func(body map[string]string) (int, map[string]string) {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/probes/renew-cert", strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.handleProbeRenewCert(w, r)
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	csr := csrFor(t, newKey)

	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if code, out := renew(map[string]string{"csr": csr, "cert": string(curPEM), "sig": signText(t, otherKey, csr)}); code != 403 {
		t.Fatalf("a request not signed by the current key: %d %v", code, out)
	}
	site2PEM, _ := ca.SignCSR([]byte(csrFor(t, otherKey)), "proxy-site2", 24*time.Hour)
	if code, out := renew(map[string]string{"csr": csr, "cert": string(site2PEM), "sig": signText(t, otherKey, csr)}); code != 403 || !strings.Contains(out["error"], "proxy-site2") {
		t.Fatalf("another probe's certificate: %d %v", code, out)
	}
	foreign := loadTestCA(t, t.TempDir())
	foreignPEM, _ := foreign.SignCSR([]byte(csrFor(t, curKey)), "proxy-site1", 24*time.Hour)
	if code, out := renew(map[string]string{"csr": csr, "cert": string(foreignPEM), "sig": signText(t, curKey, csr)}); code != 403 {
		t.Fatalf("a certificate from another CA: %d %v", code, out)
	}

	code, out := renew(map[string]string{"csr": csr, "cert": string(curPEM), "sig": signText(t, curKey, csr)})
	if code != 200 {
		t.Fatalf("the renewal: %d %v", code, out)
	}
	c, err := parseCertPEM([]byte(out["certificate"]))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "proxy-site1" || !c.PublicKey.(*rsa.PublicKey).Equal(&newKey.PublicKey) || !ca.Signed(c) {
		t.Fatalf("the new certificate: CN %q, for the new key and signed by the CA expected", c.Subject.CommonName)
	}
	if time.Until(c.NotAfter) < 4*365*24*time.Hour {
		t.Fatalf("the new certificate lasts as long as an enrolled one: %v", c.NotAfter)
	}
	if out["ca"] == "" {
		t.Fatal("the CA rides along")
	}
	ag, _ := st.ProbeAgentByName(ctx, "proxy-site1")
	if ag.CertEnd != c.NotAfter.Unix() || ag.CertRenewedAt == 0 {
		t.Fatalf("recorded end %d renewed %d", ag.CertEnd, ag.CertRenewedAt)
	}
}
