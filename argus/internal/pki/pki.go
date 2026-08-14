// Package pki loads the monitoring CA and signs probe certificate-signing requests, so Argus
// can enroll new probes: the probe generates its own keypair + CSR locally (the private key never
// leaves it) and Argus returns a signed leaf certificate trusted by the Zabbix server.
//
// This is the same CA that gen-certs.sh created and that the Zabbix server pins
// (issuer CN=Monitoring Core CA); the signed leaf uses CN=proxy-<site>, matching the manual flow.
package pki

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"
)

// CA is the loaded signing authority (certificate + private key) plus the PEM of the cert, which
// is handed back to probes as their trust anchor (ca.crt).
type CA struct {
	cert   *x509.Certificate
	key    crypto.Signer
	certPEM []byte
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
	// Note: we don't hard-require the basicConstraints CA flag here — the operator's ca.crt is
	// the same one Zabbix already trusts, and openssl's `req -x509` output varies on that bit.
	return &CA{cert: cert, key: key, certPEM: certPEM}, nil
}

// CertPEM returns the CA certificate PEM (ca.crt) to ship to a probe as its trust anchor.
func (c *CA) CertPEM() []byte { return c.certPEM }

// SubjectCN is the CA's common name (e.g. "Monitoring Core CA"), used to pin issuer in Zabbix.
func (c *CA) SubjectCN() string { return c.cert.Subject.CommonName }

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

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	notAfter := time.Now().Add(ttl)
	if notAfter.After(c.cert.NotAfter) {
		notAfter = c.cert.NotAfter
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
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
