// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package pki loads the monitoring CA and signs probe certificate-signing requests, so Argus
// can enroll new probes: the probe generates its own keypair + CSR locally (the private key never
// leaves it) and Argus returns a signed leaf certificate trusted by the Zabbix server.
//
// This is the same CA that gen-certs.sh created and that the Zabbix server pins
// (issuer CN=Monitoring Core CA); the signed leaf uses CN=proxy-<site>, matching the manual flow.
//
// The CA certificate is renewed in place on the core VM (argus-cert-renew: the same key and name,
// a later end), so Refresh picks a changed file up without a restart, and the check-in hands the
// renewed certificate to the probes.
package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"
)

// CA is the loaded signing authority (certificate + private key) plus the PEM of the cert, which
// is handed back to probes as their trust anchor (ca.crt).
type CA struct {
	certFile string
	key      crypto.Signer

	mu      sync.RWMutex
	cert    *x509.Certificate
	certPEM []byte
	stamp   fileStamp // the certificate file as last read, so Refresh reads it again only when it changed
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	if fi, err := os.Stat(path); err == nil {
		return fileStamp{fi.ModTime(), fi.Size()}
	}
	return fileStamp{}
}

// Load reads the CA certificate and private key from mounted files. Both paths must be set.
func Load(certFile, keyFile string) (*CA, error) {
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("CA cert and key files must both be configured")
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert %s: %w", certFile, err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read CA key %s: %w", keyFile, err)
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	if !sameKey(cert, key) {
		return nil, fmt.Errorf("the CA key %s does not belong to the certificate %s", keyFile, certFile)
	}
	// Note: we don't hard-require the basicConstraints CA flag here - the operator's ca.crt is
	// the same one Zabbix already trusts, and openssl's `req -x509` output varies on that bit.
	return &CA{certFile: certFile, key: key, cert: cert, certPEM: firstPEM(cert), stamp: stampOf(certFile)}, nil
}

// Refresh reads the certificate file again when it changed since it was read (the core VM renews the
// CA in place). A renewal is taken only when it is the same CA: the same name and key. It reports
// whether the certificate in use changed; a file that can't be used keeps the one in use.
func (c *CA) Refresh() (bool, error) {
	st := stampOf(c.certFile)
	c.mu.RLock()
	same := st == c.stamp
	c.mu.RUnlock()
	if same || st == (fileStamp{}) {
		return false, nil
	}
	certPEM, err := os.ReadFile(c.certFile)
	if err != nil {
		return false, fmt.Errorf("read CA cert %s: %w", c.certFile, err)
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stamp = st
	if bytes.Equal(cert.Raw, c.cert.Raw) {
		return false, nil
	}
	if !sameKey(cert, c.key) || !bytes.Equal(cert.RawSubject, c.cert.RawSubject) {
		return false, fmt.Errorf("%s now holds another CA (another name or key): keeping the one loaded at start", c.certFile)
	}
	c.cert, c.certPEM = cert, firstPEM(cert)
	return true, nil
}

// CertPEM returns the CA certificate PEM (ca.crt) to ship to a probe as its trust anchor.
func (c *CA) CertPEM() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.certPEM
}

// SubjectCN is the CA's common name (e.g. "Monitoring Core CA"), used to pin issuer in Zabbix.
func (c *CA) SubjectCN() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cert.Subject.CommonName
}

// Fingerprint is the SHA-256 of the CA certificate (lowercase hex), what a probe reports of its ca.crt
// at check-in.
func (c *CA) Fingerprint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Fingerprint(c.cert)
}

// NotAfter is the end of the CA certificate.
func (c *CA) NotAfter() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cert.NotAfter
}

// Signed reports whether this CA signed cert: its issuer is the CA's name and the CA's key made its
// signature. Time is left out, so a certificate that already ended can still prove where it came from.
func (c *CA) Signed(cert *x509.Certificate) bool {
	c.mu.RLock()
	parent := c.cert
	c.mu.RUnlock()
	return bytes.Equal(cert.RawIssuer, parent.RawSubject) && cert.CheckSignatureFrom(parent) == nil
}

// Fingerprint is a certificate's SHA-256 in lowercase hex, as `openssl x509 -fingerprint -sha256`
// prints it without the colons.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func firstPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

func sameKey(cert *x509.Certificate, key crypto.Signer) bool {
	pub, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	return ok && pub.Equal(key.Public())
}

// SignCSR validates a PEM-encoded CSR and issues a leaf certificate whose subject CommonName is
// forced to cn (so a token scoped to one proxy can't obtain a certificate for another). Only the
// public key is taken from the CSR. ttl bounds the validity (capped at the CA's own expiry).
func (c *CA) SignCSR(csrPEM []byte, cn string, ttl time.Duration) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("expected a PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature invalid: %w", err)
	}
	switch k := csr.PublicKey.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return nil, fmt.Errorf("RSA key too small (%d bits; 2048 or more)", k.N.BitLen())
		}
	case *ecdsa.PublicKey:
		if k.Curve == nil || k.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("ECDSA curve too small (P-256 or larger)")
		}
	case ed25519.PublicKey:
	default:
		return nil, fmt.Errorf("unsupported key type %T", csr.PublicKey)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	parent := c.cert
	c.mu.RUnlock()
	notAfter := time.Now().Add(ttl)
	if notAfter.After(parent.NotAfter) {
		notAfter = parent.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		// A probe is a TLS client to the Zabbix server; clientAuth only (so the leaf can't
		// double as a server certificate).
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func parseCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in CA certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseKey accepts PKCS#8, PKCS#1 (RSA), or SEC1 (EC) private keys.
func parseKey(pemBytes []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in CA key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := k.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, fmt.Errorf("CA key is not a signer")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("unsupported CA key format (want PKCS#8, PKCS#1 or SEC1)")
}
