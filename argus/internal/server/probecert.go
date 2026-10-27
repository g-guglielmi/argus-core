// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"time"

	"argus/internal/auth"
)

// A probe renews its own certificate (DESIGN 14c). Its key never leaves it, so the core can't renew
// it: thirty days before its end the probe makes a new key and a request for it, and asks Argus to
// sign it. Two things prove it is that probe: its check-in token, and its current certificate (signed
// by the monitoring CA, for its proxy name) with a signature over the request made by the current
// key. A leaked token alone gets no certificate. A certificate that already ended still proves where
// it came from, so a probe that went dark on an ended certificate comes back by itself.

// proxyCertRenewTTL is as long as an enrolled probe's certificate (capped by the CA's own end).
const proxyCertRenewTTL = proxyCertTTL

func (s *Server) handleProbeRenewCert(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this Argus holds no monitoring CA to sign with"})
		return
	}
	tok := bearerToken(r)
	if tok == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing probe token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	proxyName, err := s.st.ProbeNameByToken(ctx, auth.HashToken(tok))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe token"})
		return
	}
	// A probe asks once, and again an hour later when something failed: more than that is not a probe.
	key := "renewcert:" + proxyName
	if s.rateBlocked(w, key) {
		return
	}
	s.loginLimiter.Fail(key)
	var req struct {
		CSR  string `json:"csr"`
		Cert string `json:"cert"` // the probe's current certificate (PEM)
		Sig  string `json:"sig"`  // base64: the CSR's bytes signed by the current key (openssl dgst -sha256 -sign)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.checkProbeCertProof(proxyName, req.CSR, req.Cert, req.Sig); err != nil {
		s.logger.Warn("probe certificate renewal refused", "proxy", proxyName, "err", err)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "renewal refused: " + err.Error()})
		return
	}
	certPEM, err := s.ca.SignCSR([]byte(req.CSR), proxyName, proxyCertRenewTTL)
	if err != nil {
		s.logger.Warn("probe certificate renewal: sign CSR failed", "proxy", proxyName, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the certificate request could not be signed: " + err.Error()})
		return
	}
	end := int64(0)
	if c, err := parseCertPEM(certPEM); err == nil {
		end = c.NotAfter.Unix()
	}
	_ = s.st.MarkProbeCertRenewed(ctx, proxyName, end)
	s.logger.Info("probe certificate renewed", "proxy", proxyName, "ends", time.Unix(end, 0).UTC().Format("2006-01-02"))
	writeJSON(w, http.StatusOK, map[string]string{"certificate": string(certPEM), "ca": string(s.ca.CertPEM())})
}

// checkProbeCertProof: the current certificate is the monitoring CA's, for this proxy, and its key
// signed the request.
func (s *Server) checkProbeCertProof(proxyName, csrPEM, certPEM, sigB64 string) error {
	if strings.TrimSpace(csrPEM) == "" || strings.TrimSpace(certPEM) == "" || strings.TrimSpace(sigB64) == "" {
		return errors.New("csr, cert and sig are required")
	}
	cur, err := parseCertPEM([]byte(certPEM))
	if err != nil {
		return errors.New("the current certificate can't be read")
	}
	if !s.ca.Signed(cur) {
		return errors.New("the current certificate is not the monitoring CA's")
	}
	if cur.Subject.CommonName != proxyName {
		return errors.New("the current certificate is for " + cur.Subject.CommonName + ", not " + proxyName)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sigB64), ""))
	if err != nil {
		return errors.New("the signature is not base64")
	}
	sum := sha256.Sum256([]byte(csrPEM))
	switch pub := cur.PublicKey.(type) {
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, sum[:], sig) {
			err = errors.New("bad signature")
		}
	default:
		err = errors.New("unsupported key type")
	}
	if err != nil {
		return errors.New("the request was not signed by the current certificate's key")
	}
	return nil
}

func parseCertPEM(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
