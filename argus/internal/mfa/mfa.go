// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package mfa wraps TOTP enrollment/validation and recovery-code generation.
//
// TOTP uses the RFC 6238 defaults (SHA1, 6 digits, 30-second period). Those are exactly
// what authenticator apps and password managers (Google/Microsoft Authenticator,
// Bitwarden, 1Password, …) expect, so the QR code and the base32 "setup key" both work
// with Bitwarden's autofill without any special handling.
package mfa

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"image/png"
	"strings"

	"github.com/pquerna/otp/totp"
)

// Issuer is the label shown in authenticator apps (the "account" is the user's email).
const Issuer = "Argus"

// Enrollment is what the client needs to add Argus to an authenticator.
type Enrollment struct {
	Secret     string `json:"secret"`       // base32 key to paste into Bitwarden's TOTP field
	OtpauthURL string `json:"otpauth_url"`  // the otpauth:// URI encoded in the QR
	QRDataURI  string `json:"qr_data_uri"`  // data:image/png;base64,… for an <img>
}

// Generate creates a new random secret and a scannable QR for the given account.
func Generate(accountEmail string) (*Enrollment, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: Issuer, AccountName: accountEmail})
	if err != nil {
		return nil, err
	}
	img, err := key.Image(220, 220)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &Enrollment{
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
		QRDataURI:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// Validate reports whether a 6-digit code is currently valid for the secret.
func Validate(code, secret string) bool {
	return totp.Validate(strings.TrimSpace(code), secret)
}

// GenerateRecoveryCodes returns n human-friendly codes and their storage hashes.
func GenerateRecoveryCodes(n int) (plain, hashes []string, err error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for i := 0; i < n; i++ {
		b := make([]byte, 10) // 80 bits -> 16 base32 chars
		if _, err = rand.Read(b); err != nil {
			return nil, nil, err
		}
		s := strings.ToLower(enc.EncodeToString(b))
		code := s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
		plain = append(plain, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return plain, hashes, nil
}

// HashRecoveryCode normalizes a code (case- and dash-insensitive) and returns its SHA-256 hex.
func HashRecoveryCode(code string) string {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}
