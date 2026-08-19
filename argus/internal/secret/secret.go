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
	"io"
	"os"
	"path/filepath"
	"strings"
)

const marker = "enc:v1:"

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
// (mode 0600) so encryption is on by default with zero configuration.
func Load(envKey, dataDir string) (*Cipher, string, error) {
	if strings.TrimSpace(envKey) != "" {
		sum := sha256.Sum256([]byte(envKey))
		c, err := newFromKey(sum[:])
		return c, "env", err
	}
	path := filepath.Join(dataDir, "secret.key")
	if b, err := os.ReadFile(path); err == nil {
		if key, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(key) == 32 {
			c, err := newFromKey(key)
			return c, "keyfile", err
		}
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
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
		return plaintext
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return marker + base64.StdEncoding.EncodeToString(ct)
}

// Decrypt reverses Encrypt. Unmarked (plaintext) values pass through, as do marked values when the
// cipher is unavailable or the payload is corrupt (returned as-is rather than panicking).
func (c *Cipher) Decrypt(s string) string {
	if !IsEncrypted(s) || c == nil || !c.enabled {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, marker))
	if err != nil {
		return s
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return s
	}
	pt, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return s
	}
	return string(pt)
}
