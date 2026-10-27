// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSigned makes a CA certificate for key, valid from now for days (what gen-certs.sh and the
// core VM's first boot make, and what argus-cert-renew makes again from the same key).
func selfSigned(t *testing.T, key *rsa.PrivateKey, cn string, serial int64, days int) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(days) * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeCA(t *testing.T, dir string, certPEM []byte, key *rsa.PrivateKey) (string, string) {
	t.Helper()
	crt, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := os.WriteFile(crt, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return crt, keyFile
}

func probeCSR(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "proxy"}}, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// replace writes the file again with a later modification time, as a renewal on the core VM does.
func replace(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func verifies(leafPEM, caPEM []byte) error {
	b, _ := pem.Decode(leafPEM)
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

// A CA renewed in place (the same key and name, a later end) is picked up without a restart, and a
// probe certificate signed before the renewal is still trusted by the renewed CA: nothing re-enrolls.
func TestRefreshTakesTheRenewedCAAndOldCertificatesStayTrusted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	crt, keyFile := writeCA(t, t.TempDir(), selfSigned(t, key, "Monitoring Core CA", 1, 30), key)
	ca, err := Load(crt, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := ca.Refresh(); changed || err != nil {
		t.Fatalf("an unchanged file: changed=%v err=%v", changed, err)
	}
	leaf, err := ca.SignCSR(probeCSR(t), "proxy-site1", 5*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if end := ca.NotAfter(); time.Until(end) > 31*24*time.Hour {
		t.Fatalf("the old CA ends in 30 days, got %v", end)
	}
	oldFP := ca.Fingerprint()

	replace(t, crt, selfSigned(t, key, "Monitoring Core CA", 2, 3650))
	changed, err := ca.Refresh()
	if !changed || err != nil {
		t.Fatalf("the renewed CA: changed=%v err=%v", changed, err)
	}
	if time.Until(ca.NotAfter()) < 3000*24*time.Hour {
		t.Fatalf("the renewed CA's end was not taken: %v", ca.NotAfter())
	}
	if ca.Fingerprint() == oldFP || len(ca.Fingerprint()) != 64 {
		t.Fatalf("fingerprint %q (was %q)", ca.Fingerprint(), oldFP)
	}
	if err := verifies(leaf, ca.CertPEM()); err != nil {
		t.Fatalf("a probe certificate from before the renewal is not trusted by the renewed CA: %v", err)
	}
	// New enrollments are no longer cut short by the old end.
	fresh, err := ca.SignCSR(probeCSR(t), "proxy-site2", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(fresh)
	c, _ := x509.ParseCertificate(b.Bytes)
	if time.Until(c.NotAfter) < 360*24*time.Hour {
		t.Fatalf("a new probe certificate is still capped by the old CA's end: %v", c.NotAfter)
	}
}

// A file that now holds another CA (another key or name) is refused: the one in use stays.
func TestRefreshRefusesAnotherCA(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	crt, keyFile := writeCA(t, t.TempDir(), selfSigned(t, key, "Monitoring Core CA", 1, 30), key)
	ca, err := Load(crt, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	fp := ca.Fingerprint()
	for name, body := range map[string][]byte{
		"another key":  selfSigned(t, other, "Monitoring Core CA", 2, 3650),
		"another name": selfSigned(t, key, "Someone Else CA", 3, 3650),
	} {
		replace(t, crt, body)
		if changed, err := ca.Refresh(); changed || err == nil {
			t.Fatalf("%s: changed=%v err=%v", name, changed, err)
		}
		if ca.Fingerprint() != fp {
			t.Fatalf("%s: the CA in use changed", name)
		}
	}
}

func TestLoadRefusesAKeyOfAnotherCertificate(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	crt, keyFile := writeCA(t, t.TempDir(), selfSigned(t, key, "Monitoring Core CA", 1, 30), other)
	if _, err := Load(crt, keyFile); err == nil {
		t.Fatal("a key that does not belong to the certificate was loaded")
	}
}
