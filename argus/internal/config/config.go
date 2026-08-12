package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds runtime configuration, all sourced from environment variables so the
// container is configured purely via `docker run -e ...` / --env-file.
type Config struct {
	Listen         string // ARGUS_LISTEN, e.g. ":8080"
	ZabbixAPIURL   string // ARGUS_ZABBIX_API_URL
	ZabbixAPIToken string // ARGUS_ZABBIX_API_TOKEN, for authenticated read calls
	DataDir        string // ARGUS_DATA_DIR, SQLite + CA store live here (mounted volume)
	PublicURL      string // ARGUS_PUBLIC_URL, external base URL for links in notifications
	AdminEmail    string // ARGUS_ADMIN_EMAIL, used once to seed the first admin
	AdminPassword string // ARGUS_ADMIN_PASSWORD, used once to seed the first admin
	CookieSecure  bool   // ARGUS_COOKIE_SECURE, set true when served over HTTPS

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
		AdminEmail:    env("ARGUS_ADMIN_EMAIL", ""),
		AdminPassword: env("ARGUS_ADMIN_PASSWORD", ""),
		CookieSecure:  envBool("ARGUS_COOKIE_SECURE", false),
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
