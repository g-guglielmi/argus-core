// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package unifi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// How a controller's certificate is checked. Consoles ship self-signed certificates, so plain
// verification against the system roots fails for most of them; pinning the certificate's
// SHA-256 (learned once and confirmed by the admin) makes a later swap fail loudly instead.
const (
	TLSVerify = "verify" // system roots
	TLSPin    = "pin"    // the leaf certificate's SHA-256 must match Options.Fingerprint
	TLSIgnore = "ignore" // no check at all
)

// Options is how to talk to one controller.
type Options struct {
	TLSMode     string // TLSVerify (default), TLSPin, TLSIgnore
	Fingerprint string // hex SHA-256 of the leaf certificate, for TLSPin
}

// NormalizeFingerprint lowercases and strips separators from a fingerprint as people paste it.
func NormalizeFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s)))
}

func tlsConfigFor(o Options) *tls.Config {
	switch o.TLSMode {
	case TLSIgnore:
		return &tls.Config{InsecureSkipVerify: true}
	case TLSPin:
		want := NormalizeFingerprint(o.Fingerprint)
		return &tls.Config{
			InsecureSkipVerify: true, // the chain isn't the check; the leaf's digest is
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("the controller presented no certificate")
				}
				got := Fingerprint(rawCerts[0])
				if got != want {
					return fmt.Errorf("the controller's certificate changed (now %s); re-pin it in Discovery settings if that was expected", got)
				}
				return nil
			},
		}
	default:
		return &tls.Config{}
	}
}

// Fingerprint is the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func newHTTPClient(o Options) *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfigFor(o),
		},
	}
}

// CertInfo describes the certificate a controller presents, for the admin to decide about.
type CertInfo struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotAfter    time.Time `json:"not_after"`
	Trusted     bool      `json:"trusted"` // verifies against the system roots for this host
}

// Inspect connects to the controller's address and reports its certificate: what it is and
// whether the system roots trust it. Nothing is sent but the handshake.
func Inspect(ctx context.Context, baseURL string) (CertInfo, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return CertInfo{}, fmt.Errorf("invalid controller URL")
	}
	if u.Scheme != "https" {
		return CertInfo{}, fmt.Errorf("not an https URL")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	d := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, port), &tls.Config{InsecureSkipVerify: true, ServerName: host})
	if err != nil {
		return CertInfo{}, err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return CertInfo{}, fmt.Errorf("the controller presented no certificate")
	}
	leaf := certs[0]
	info := CertInfo{Fingerprint: Fingerprint(leaf.Raw), Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(), NotAfter: leaf.NotAfter}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err == nil {
		info.Trusted = true
	}
	_ = ctx
	return info, nil
}
