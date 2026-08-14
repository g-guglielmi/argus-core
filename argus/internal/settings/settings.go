// Package settings manages the subset of configuration that an admin can change at runtime
// from the UI (Zabbix connection, public URL, timezone, login rate limits).
//
// Precedence is env-wins: if the backing environment variable is set, that value is used and
// the field is read-only in the UI ("managed via environment"). Otherwise the value stored in
// the database (app_meta) is authoritative, falling back to a built-in default. This keeps an
// existing `docker run … -e ARGUS_*` deployment working unchanged while letting operators move
// individual settings into the GUI by dropping the env var.
package settings

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/ratelimit"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Setting keys (also the JSON keys used by the API and the app_meta suffix).
const (
	KeyZabbixURL   = "zabbix_url"
	KeyZabbixToken = "zabbix_token"
	KeyPublicURL   = "public_url"
	KeyTimezone    = "timezone"
	KeyLoginMax    = "login_max_attempts"
	KeyLoginWindow = "login_window_minutes"
)

const metaPrefix = "setting:"

// def describes one runtime setting.
type def struct {
	key    string
	env    string // backing environment variable (env-set ⇒ locked)
	label  string
	group  string
	typ    string // "url" | "text" | "int" | "tz"
	secret bool
	def    string // built-in default when neither env nor DB provide a value
	hint   string
}

var defs = []def{
	{KeyZabbixURL, "ARGUS_ZABBIX_API_URL", "Zabbix API URL", "Connection", "url", false, "", "JSON-RPC endpoint, e.g. http://10.0.0.10:8080/api_jsonrpc.php"},
	{KeyZabbixToken, "ARGUS_ZABBIX_API_TOKEN", "Zabbix API token", "Connection", "text", true, "", "Bearer token with write scope (for acknowledge/pause). Leave blank to keep the current value."},
	{KeyPublicURL, "ARGUS_PUBLIC_URL", "Public URL", "General", "url", false, "", "External base URL, used for Open/Acknowledge links in notifications."},
	{KeyTimezone, "ARGUS_TZ", "Timezone", "General", "tz", false, "UTC", "IANA name for notification timestamps, e.g. Europe/Rome."},
	{KeyLoginMax, "ARGUS_LOGIN_MAX_ATTEMPTS", "Login max attempts", "Security", "int", false, "7", "Failed sign-ins per window before throttling."},
	{KeyLoginWindow, "ARGUS_LOGIN_WINDOW_MINUTES", "Login window (minutes)", "Security", "int", false, "15", "Sliding window for the attempt counter."},
}

func defFor(key string) (def, bool) {
	for _, d := range defs {
		if d.key == key {
			return d, true
		}
	}
	return def{}, false
}

// resolved is the computed state of one setting for display.
type resolved struct {
	value    string // effective value (empty for secrets — never exposed)
	source   string // "env" | "stored" | "default"
	locked   bool   // env-set ⇒ not editable in the UI
	hasValue bool   // for secrets: whether a value is currently set
}

// View is the JSON shape returned to the admin UI.
type View struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Type     string `json:"type"`
	Secret   bool   `json:"secret"`
	Hint     string `json:"hint"`
	Env      string `json:"env"` // backing env var (shown when the field is env-locked)
	Value    string `json:"value"`
	Source   string `json:"source"`
	Locked   bool   `json:"locked"`
	HasValue bool   `json:"has_value"`
}

// Manager holds the effective runtime settings and applies changes to the live subsystems.
type Manager struct {
	st      *store.Store
	zbx     *zabbix.Client
	limiter *ratelimit.Limiter

	mu        sync.RWMutex
	snap      map[string]resolved
	publicURL string
	loc       *time.Location
}

// New builds the manager, creates the login limiter, loads any stored overrides, and applies
// the effective values to the Zabbix client and limiter. Env values take precedence.
func New(ctx context.Context, st *store.Store, zbx *zabbix.Client) (*Manager, error) {
	m := &Manager{
		st:      st,
		zbx:     zbx,
		limiter: ratelimit.New(7, 15*time.Minute), // reconfigured immediately by apply()
		loc:     time.UTC,
	}
	if err := m.reload(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Limiter() *ratelimit.Limiter { return m.limiter }

func (m *Manager) PublicURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.publicURL
}

func (m *Manager) Location() *time.Location {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loc
}

// List returns every setting's current state for the admin UI.
func (m *Manager) List() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(defs))
	for _, d := range defs {
		r := m.snap[d.key]
		out = append(out, View{
			Key: d.key, Label: d.label, Group: d.group, Type: d.typ, Secret: d.secret,
			Hint: d.hint, Env: d.env, Value: r.value, Source: r.source, Locked: r.locked, HasValue: r.hasValue,
		})
	}
	return out
}

// Set validates, persists, and re-applies a batch of updates. Env-locked keys are rejected.
// For non-secret keys an empty value reverts to the default; for secrets an empty value leaves
// the stored secret unchanged.
func (m *Manager) Set(ctx context.Context, updates map[string]string) error {
	// Validate everything before writing anything.
	type change struct {
		d     def
		val   string
		clear bool // revert to default (delete the stored override)
		skip  bool // secret left blank ⇒ unchanged
	}
	changes := make([]change, 0, len(updates))
	for key, raw := range updates {
		d, ok := defFor(key)
		if !ok {
			return fmt.Errorf("unknown setting %q", key)
		}
		if envSet(d.env) {
			return fmt.Errorf("%q is managed via %s; unset that environment variable to edit it here", d.label, d.env)
		}
		val := strings.TrimSpace(raw)
		if d.secret {
			if val == "" {
				changes = append(changes, change{d: d, skip: true})
				continue
			}
			changes = append(changes, change{d: d, val: val})
			continue
		}
		if val == "" {
			changes = append(changes, change{d: d, clear: true})
			continue
		}
		if err := validate(d, val); err != nil {
			return err
		}
		changes = append(changes, change{d: d, val: normalize(d, val)})
	}

	for _, c := range changes {
		mk := metaPrefix + c.d.key
		switch {
		case c.skip:
			// leave the stored secret as-is
		case c.clear:
			if err := m.st.MetaDelete(ctx, mk); err != nil {
				return err
			}
		case c.d.secret:
			if err := m.st.MetaSetSecret(ctx, mk, c.val); err != nil {
				return err
			}
		default:
			if err := m.st.MetaSet(ctx, mk, c.val); err != nil {
				return err
			}
		}
	}
	return m.reload(ctx)
}

// reload recomputes every effective value from env + DB and applies it to the live subsystems.
func (m *Manager) reload(ctx context.Context) error {
	snap := make(map[string]resolved, len(defs))
	for _, d := range defs {
		r, err := m.resolve(ctx, d)
		if err != nil {
			return err
		}
		snap[d.key] = r
	}

	// Effective scalar values (empty falls through to the built-in defaults already in snap).
	zURL := effective(snap[KeyZabbixURL])
	zTok := m.effectiveSecret(ctx, KeyZabbixToken)
	pub := effective(snap[KeyPublicURL])
	tz := effective(snap[KeyTimezone])
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	maxN := atoiOr(effective(snap[KeyLoginMax]), 7)
	winMin := atoiOr(effective(snap[KeyLoginWindow]), 15)

	// Apply to the live subsystems (each is independently lock-guarded).
	m.zbx.Configure(zURL, zTok)
	m.limiter.Configure(maxN, time.Duration(winMin)*time.Minute)

	m.mu.Lock()
	m.snap = snap
	m.publicURL = pub
	m.loc = loc
	m.mu.Unlock()
	return nil
}

// resolve computes the display state for one setting (env → stored → default).
func (m *Manager) resolve(ctx context.Context, d def) (resolved, error) {
	if v, ok := os.LookupEnv(d.env); ok && strings.TrimSpace(v) != "" {
		v = normalize(d, strings.TrimSpace(v))
		if d.secret {
			return resolved{source: "env", locked: true, hasValue: true}, nil
		}
		return resolved{value: v, source: "env", locked: true, hasValue: true}, nil
	}
	var raw string
	var ok bool
	var err error
	if d.secret {
		raw, ok, err = m.st.MetaGetSecret(ctx, metaPrefix+d.key)
	} else {
		raw, ok, err = m.st.MetaGet(ctx, metaPrefix+d.key)
	}
	if err != nil {
		return resolved{}, err
	}
	if ok && raw != "" {
		if d.secret {
			return resolved{source: "stored", hasValue: true}, nil
		}
		return resolved{value: raw, source: "stored", hasValue: true}, nil
	}
	return resolved{value: d.def, source: "default", hasValue: d.def != ""}, nil
}

// effective returns the plain effective value for a non-secret resolved setting.
func effective(r resolved) string { return r.value }

// effectiveSecret fetches the actual secret value (env or decrypted DB) — never surfaced to the UI.
func (m *Manager) effectiveSecret(ctx context.Context, key string) string {
	d, _ := defFor(key)
	if v, ok := os.LookupEnv(d.env); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if raw, ok, err := m.st.MetaGetSecret(ctx, metaPrefix+key); err == nil && ok {
		return raw
	}
	return ""
}

func envSet(name string) bool {
	v, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(v) != ""
}

func normalize(d def, v string) string {
	if d.typ == "url" {
		return strings.TrimRight(v, "/")
	}
	return v
}

func validate(d def, v string) error {
	switch d.typ {
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be a full http(s) URL", d.label)
		}
	case "tz":
		if _, err := time.LoadLocation(v); err != nil {
			return fmt.Errorf("%q is not a valid IANA timezone", v)
		}
	case "int":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("%s must be a whole number ≥ 1", d.label)
		}
	}
	return nil
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 1 {
		return n
	}
	return def
}
