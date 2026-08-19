package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration, all sourced from environment variables so the
// container is configured purely via `docker run -e ...` / --env-file.
type Config struct {
	Listen         string // ARGUS_LISTEN, e.g. ":8080"
	ZabbixAPIURL   string // ARGUS_ZABBIX_API_URL
	ZabbixAPIToken string // ARGUS_ZABBIX_API_TOKEN, for authenticated read calls
	DataDir        string // ARGUS_DATA_DIR, SQLite + CA store live here (mounted volume)
	PublicURL      string // ARGUS_PUBLIC_URL, external base URL for links in notifications
	TimeZone       string // ARGUS_TZ, IANA name for timestamps in notifications (default UTC)
	SecretKey      string // ARGUS_SECRET_KEY, encrypts stored secrets at rest (empty = keyfile)
	AdminEmail    string // ARGUS_ADMIN_EMAIL, used once to seed the first admin
	AdminPassword string // ARGUS_ADMIN_PASSWORD, used once to seed the first admin
	CookieSecure  bool   // ARGUS_COOKIE_SECURE, set true when served over HTTPS

	// Login rate limiting (brute-force protection).
	LoginMaxAttempts int           // ARGUS_LOGIN_MAX_ATTEMPTS, failures before a temporary block
	LoginWindow      time.Duration // ARGUS_LOGIN_WINDOW_MINUTES, the sliding window
	TrustProxy       bool          // ARGUS_TRUST_PROXY, use X-Forwarded-For for the client IP

	// Probe enrollment (token-based PKI). When the CA files are mounted, Argus can sign probe
	// CSRs and register their proxies in Zabbix. CAKeyFile is the crown jewel - mount read-only.
	// (ARGUS_PROBE_CORE_HOST is resolved by the settings manager, so it can be edited in the UI.)
	CACertFile string // ARGUS_CA_CERT_FILE, path to the monitoring CA certificate (ca.crt)
	CAKeyFile  string // ARGUS_CA_KEY_FILE, path to the CA private key (ca.key)

	// WebAuthn / passkeys. Passkeys need a real domain (never a bare IP) and HTTPS,
	// so they're only active when RPID and at least one origin are configured.
	RPID          string   // ARGUS_RP_ID, e.g. "monitoring.example.com"
	RPDisplayName string   // ARGUS_RP_DISPLAY_NAME, shown by the authenticator
	RPOrigins     []string // ARGUS_RP_ORIGINS, comma-separated, e.g. "https://monitoring.example.com"
}

// PasskeysEnabled reports whether WebAuthn is configured well enough to offer passkeys.
func (c Config) PasskeysEnabled() bool {
	return c.RPID != "" && len(c.RPOrigins) > 0
}

func Load() Config {
	return Config{
		Listen:         env("ARGUS_LISTEN", ":8080"),
		ZabbixAPIURL:   env("ARGUS_ZABBIX_API_URL", ""),
		ZabbixAPIToken: env("ARGUS_ZABBIX_API_TOKEN", ""),
		DataDir:        env("ARGUS_DATA_DIR", "/data"),
		PublicURL:      strings.TrimRight(env("ARGUS_PUBLIC_URL", ""), "/"),
		TimeZone:       env("ARGUS_TZ", "UTC"),
		SecretKey:      env("ARGUS_SECRET_KEY", ""),
		AdminEmail:    env("ARGUS_ADMIN_EMAIL", ""),
		AdminPassword: env("ARGUS_ADMIN_PASSWORD", ""),
		CookieSecure:  envBool("ARGUS_COOKIE_SECURE", false),
		LoginMaxAttempts: envInt("ARGUS_LOGIN_MAX_ATTEMPTS", 7),
		LoginWindow:      time.Duration(envInt("ARGUS_LOGIN_WINDOW_MINUTES", 15)) * time.Minute,
		TrustProxy:       envBool("ARGUS_TRUST_PROXY", false),
		CACertFile:       env("ARGUS_CA_CERT_FILE", ""),
		CAKeyFile:        env("ARGUS_CA_KEY_FILE", ""),
		RPID:          env("ARGUS_RP_ID", ""),
		RPDisplayName: env("ARGUS_RP_DISPLAY_NAME", "Argus"),
		RPOrigins:     envList("ARGUS_RP_ORIGINS"),
	}
}

// envList splits a comma-separated env var into trimmed, non-empty values.
func envList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// DBPath is the SQLite database file location inside the data volume.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "argus.db") }

// Location resolves the configured IANA timezone, falling back to UTC on an unknown name.
// (The binary imports time/tzdata so this works on a distroless image without a system tz db.)
func (c Config) Location() *time.Location {
	if loc, err := time.LoadLocation(c.TimeZone); err == nil {
		return loc
	}
	return time.UTC
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
