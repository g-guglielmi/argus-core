// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package secret provides authenticated encryption (AES-256-GCM) for sensitive values stored at
// rest - notification channel credentials, TOTP seeds, and the alert-link signing key.
//
// Encrypted values are stored as "enc:v1:<base64(nonce||ciphertext)>". A nil/disabled cipher is a
// safe passthrough (returns input unchanged), and Decrypt leaves unmarked (plaintext) values as-is,
// so encryption can be introduced on an existing database without a hard migration.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const marker = "enc:v1:"

// Marker is the prefix every encrypted value carries.
const Marker = marker

// ErrWrongKey is returned when a marked value doesn't open with this cipher: the key changed, or
// the value is corrupt.
var ErrWrongKey = errors.New("stored secret does not open with the current key")

type Cipher struct {
	aead    cipher.AEAD
	enabled bool
}

func newFromKey(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, enabled: true}, nil
}

// Load resolves the encryption key and returns a cipher plus a short source label for logging.
// ARGUS_SECRET_KEY (any string, hashed to 32 bytes) takes precedence and keeps the key off the
// data volume. Otherwise a random key is generated once and persisted to <dataDir>/secret.key
// (mode 0600) so encryption is on by default with zero configuration. An existing keyfile that
// can't be read as a key is an error, never replaced: a new key would strand everything the old
// one encrypted, and the file is the only copy.
func Load(envKey, dataDir string) (*Cipher, string, error) {
	if strings.TrimSpace(envKey) != "" {
		sum := sha256.Sum256([]byte(envKey))
		c, err := newFromKey(sum[:])
		return c, "env", err
	}
	path := filepath.Join(dataDir, "secret.key")
	b, err := os.ReadFile(path)
	if err == nil {
		key, derr := hex.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil || len(key) != 32 {
			return nil, "", fmt.Errorf("%s is not a valid key (64 hex characters); restore it from a backup, or move it away to start over with new secrets", path)
		}
		c, err := newFromKey(key)
		return c, "keyfile", err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write([]byte(hex.EncodeToString(key))); err != nil {
		f.Close()
		return nil, "", err
	}
	if err := f.Close(); err != nil {
		return nil, "", err
	}
	c, err := newFromKey(key)
	return c, "keyfile (generated)", err
}

func (c *Cipher) Enabled() bool { return c != nil && c.enabled }

// IsEncrypted reports whether a stored value carries the encryption marker.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, marker) }

// Encrypt returns the marked ciphertext, or the input unchanged when disabled, empty, or already
// encrypted (so it is safe to call on values that may or may not need it).
func (c *Cipher) Encrypt(plaintext string) string {
	if c == nil || !c.enabled || plaintext == "" || IsEncrypted(plaintext) {
		return plaintext
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		// crypto/rand doesn't fail on any supported platform; if it ever does, storing the secret in
		// the clear is not an acceptable fallback.
		panic("secret: random nonce: " + err.Error())
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return marker + base64.StdEncoding.EncodeToString(ct)
}

// TryDecrypt reverses Encrypt. Unmarked (plaintext) values pass through. A marked value that
// doesn't open (wrong key, corrupt payload, cipher disabled) is ErrWrongKey.
func (c *Cipher) TryDecrypt(s string) (string, error) {
	if !IsEncrypted(s) {
		return s, nil
	}
	if c == nil || !c.enabled {
		return "", ErrWrongKey
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, marker))
	if err != nil {
		return "", ErrWrongKey
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrWrongKey
	}
	pt, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", ErrWrongKey
	}
	return string(pt), nil
}

// Decrypt is TryDecrypt for callers that treat an unreadable secret as absent: it returns "" for
// a marked value that doesn't open, never the ciphertext (which would then be used as a password,
// a token, an HMAC key). Startup verifies the key against a canary, so this only happens on a
// corrupt row.
func (c *Cipher) Decrypt(s string) string {
	pt, err := c.TryDecrypt(s)
	if err != nil {
		return ""
	}
	return pt
}
