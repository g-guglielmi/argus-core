// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

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
	"net"
	"net/url"
	"os"
	"regexp"
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
	KeyZabbixURL     = "zabbix_url"
	KeyZabbixToken   = "zabbix_token"
	KeyPublicURL     = "public_url"
	KeyTimezone      = "timezone"
	KeyLoginMax      = "login_max_attempts"
	KeyLoginWindow   = "login_window_minutes"
	KeyProbeCoreHost = "probe_core_host"
	KeySessionMax    = "session_max_hours"
	KeySessionIdle   = "session_idle_minutes"
	KeyAllowedHosts  = "allowed_hosts"
	KeyAlertDelay    = "alert_delay_seconds"
	KeyTrustProxy    = "trust_proxy"
	KeyTimeFormat    = "time_format"
	KeyAutoscale     = "probe_autoscale"
	KeyHeartbeatURL  = "heartbeat_url"
	KeyChangesKeep   = "changes_keep_days"
)

// Probe process autoscaling modes (KeyAutoscale).
const (
	AutoscaleRestart     = "restart"      // change the counts and have the updater sidecar restart the probe
	AutoscaleNextRestart = "next-restart" // change the counts; they apply whenever the probe next starts
	AutoscaleOff         = "off"          // leave the counts as they are
)

// choiceOptions lists the allowed values of the "choice" settings (the UI shows a select).
var choiceOptions = map[string][]string{
	KeyTimeFormat:  {"24h", "12h"},
	KeyAutoscale:   {AutoscaleRestart, AutoscaleNextRestart, AutoscaleOff},
	KeyChangesKeep: {"90", "365", "730"},
}

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
	min    int // minimum for "int" settings (ignored otherwise; 0 allows a disabling zero)
}

var defs = []def{
	{KeyZabbixURL, "ARGUS_ZABBIX_API_URL", "Zabbix API URL", "Connection", "url", false, "", "JSON-RPC endpoint, e.g. http://10.0.0.10:8080/api_jsonrpc.php", 0},
	{KeyZabbixToken, "ARGUS_ZABBIX_API_TOKEN", "Zabbix API token", "Connection", "text", true, "", "Bearer token with write scope (for acknowledge/pause). Leave blank to keep the current value.", 0},
	{KeyPublicURL, "ARGUS_PUBLIC_URL", "Public URL", "General", "url", false, "", "External base URL, used for Open/Acknowledge links in notifications.", 0},
	{KeyTimezone, "ARGUS_TZ", "Timezone", "General", "tz", false, "UTC", "IANA name, e.g. Europe/Rome - used for notification timestamps AND applied to the core VM's clock by its host timer.", 0},
	{KeyLoginMax, "ARGUS_LOGIN_MAX_ATTEMPTS", "Login max attempts", "Security", "int", false, "7", "Failed sign-ins per window before throttling.", 1},
	{KeyLoginWindow, "ARGUS_LOGIN_WINDOW_MINUTES", "Login window (minutes)", "Security", "int", false, "15", "Sliding window for the attempt counter.", 1},
	{KeySessionMax, "ARGUS_SESSION_MAX_HOURS", "Max session length (hours)", "Sessions", "int", false, "12", "Absolute lifetime of a sign-in before it must re-authenticate.", 1},
	{KeySessionIdle, "ARGUS_SESSION_IDLE_MINUTES", "Idle timeout (minutes)", "Sessions", "int", false, "0", "Sign out after this long with no activity. 0 disables the idle timeout.", 0},
	{KeyAllowedHosts, "ARGUS_TRUSTED_ORIGINS", "FQDNs and IPs", "Access", "hostlist", false, "", "The FQDNs or IPs people type in the browser's address bar to open Argus, comma-separated (a pasted URL is reduced to its host). Empty turns the check off (any address works). The Public URL's host and localhost are always allowed, and probes are never checked. To recover from a lockout, set ARGUS_TRUSTED_ORIGINS=* and restart.", 0},
	{KeyTimeFormat, "ARGUS_TIME_FORMAT", "Time format", "General", "choice", false, "24h", "How clocks read in Argus and on status pages: 24h (16:43) or 12h (4:43 PM).", 0},
	{KeyAlertDelay, "ARGUS_ALERT_DELAY_SECONDS", "Alert delay (seconds)", "Alerting", "int", false, "60", "How long a problem must last before anyone is notified, so a brief blip doesn't alert. 0 alerts at once. \"No data\" alerts skip it: their own period already is the wait.", 0},
	{KeyTrustProxy, "ARGUS_TRUST_PROXY", "Trusted proxies", "Proxy", "proxylist", false, "", "Leave empty when people reach Argus directly. true = one reverse proxy on the LAN or the same host in front of Argus (the client is the address it adds to X-Forwarded-For; a connection from a public address is taken as a direct client). Or list the proxies' addresses or networks, comma-separated, e.g. 10.0.0.2, 10.0.5.0/24: forwarded headers then count only from them, and a chain of proxies (NetScaler -> HAProxy -> Argus) resolves to the real client.", 0},
	{KeyProbeCoreHost, "ARGUS_PROBE_CORE_HOST", "Probe core host", "Probes", "host", false, "", "Address probes dial for :10051 (host or host:port). Prefer an IP: the proxy re-resolves this on every data send, so an FQDN here generates heavy DNS load. Baked into new enrollments and re-synced to existing probes at their next restart. Falls back to the Public URL host if empty.", 0},
	{KeyHeartbeatURL, "ARGUS_HEARTBEAT_URL", "Heartbeat URL", "Watchdog", "url", false, "", "An outside monitor's ping URL, e.g. a healthchecks.io check or an Uptime Kuma push monitor. Argus requests it once a minute while it is healthy end to end, so the monitor alerts you when the pings stop. Empty turns it off.", 0},
	{KeyChangesKeep, "ARGUS_CHANGES_KEEP_DAYS", "Keep changes for", "Changes", "choice", false, "365", "How long the change log keeps who changed what. Older entries are dropped once a day. Journal entries stay as long as their host.", 0},
	{KeyAutoscale, "ARGUS_PROBE_AUTOSCALE", "Process autoscaling", "Probes", "choice", false, AutoscaleRestart, "Zabbix starts a fixed number of pingers, pollers, trappers and workers and reads them only at start. Argus watches how busy each kind is on every probe and raises a count whose busiest hour passed 60% (aiming for 50%), or lowers one that stayed under 20%, never below the image's own default. Counts set on the container (ZBX_START* variables) are left alone. With the updater sidecar the probe restarts to apply a change (a few seconds; collected data is kept); otherwise it applies at the probe's next start.", 0},
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
	value    string // effective value (empty for secrets - never exposed)
	source   string // "env" | "stored" | "default"
	locked   bool   // env-set ⇒ not editable in the UI
	hasValue bool   // for secrets: whether a value is currently set
}

// View is the JSON shape returned to the admin UI.
type View struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Type     string   `json:"type"`
	Secret   bool     `json:"secret"`
	Min      int      `json:"min"`               // minimum for int inputs (0 allows a disabling zero)
	Options  []string `json:"options,omitempty"` // the allowed values of a "choice" setting
	Hint     string   `json:"hint"`
	Env      string   `json:"env"` // backing env var (shown when the field is env-locked)
	Value    string   `json:"value"`
	Source   string   `json:"source"`
	Locked   bool     `json:"locked"`
	HasValue bool     `json:"has_value"`
}

// Manager holds the effective runtime settings and applies changes to the live subsystems.
type Manager struct {
	st      *store.Store
	zbx     *zabbix.Client
	limiter *ratelimit.Limiter

	mu            sync.RWMutex
	snap          map[string]resolved
	publicURL     string
	loc           *time.Location
	probeCoreHost string
	sessionMax    time.Duration
	sessionIdle   time.Duration
	allowedHosts  []string // nil = the allowed-hosts check is off
	alertDelay    time.Duration
	trustProxy    TrustProxy
	clock24h      bool
	autoscale     string // probe process autoscaling mode
	heartbeatURL  string // the outside monitor's ping URL, "" = off
	changesKeep   time.Duration
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

// ConfiguredTimezone returns the effective timezone and whether it was explicitly configured
// (env or stored). The built-in UTC default reports false, so a caller mirroring the zone to
// the core VM never overwrites a first-boot timezone with an unconfigured default.
func (m *Manager) ConfiguredTimezone() (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.snap[KeyTimezone]
	if !ok || r.value == "" || r.source == "default" {
		return r.value, false
	}
	return r.value, true
}

// ProbeCoreHost is the address probes should dial for the Zabbix server (:10051), or "" to let
// the caller fall back to the Public URL host.
func (m *Manager) ProbeCoreHost() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.probeCoreHost
}

// SessionMaxLifetime is the absolute lifetime granted to a new session.
func (m *Manager) SessionMaxLifetime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionMax
}

// SessionIdleTimeout is the sliding inactivity window before a session is dropped (0 = disabled).
func (m *Manager) SessionIdleTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionIdle
}

// AllowedHosts is the configured allow-list of browser-facing hostnames, normalized; nil means the
// check is off. The Public URL's host and loopback are allowed on top of it (see server.hostAllowed).
func (m *Manager) AllowedHosts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allowedHosts
}

// AlertDelay is how long a problem must persist before the notifier alerts on it (flap guard).
func (m *Manager) AlertDelay() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.alertDelay
}

// Clock24h reports whether clocks read 24-hour (the default) rather than 12-hour.
func (m *Manager) Clock24h() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clock24h
}

// ChangesKeep is how long the change log keeps its entries.
func (m *Manager) ChangesKeep() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.changesKeep <= 0 {
		return 365 * 24 * time.Hour
	}
	return m.changesKeep
}

// ProbeAutoscale is the probe process autoscaling mode: AutoscaleRestart, AutoscaleNextRestart or
// AutoscaleOff.
func (m *Manager) ProbeAutoscale() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.autoscale == "" {
		return AutoscaleRestart
	}
	return m.autoscale
}

// HeartbeatURL is the outside monitor Argus pings while healthy, or "" when the heartbeat is off.
func (m *Manager) HeartbeatURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.heartbeatURL
}

// TrustProxy is which reverse proxies Argus believes about the client, host and scheme.
func (m *Manager) TrustProxy() TrustProxy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trustProxy
}

// List returns every setting's current state for the admin UI.
func (m *Manager) List() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(defs))
	for _, d := range defs {
		r := m.snap[d.key]
		out = append(out, View{
			Key: d.key, Label: d.label, Group: d.group, Type: d.typ, Secret: d.secret, Min: d.min, Options: choiceOptions[d.key],
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
		if nv := normalize(d, val); nv != "" {
			changes = append(changes, change{d: d, val: nv})
		} else { // "*" for the host list: stored as the default, i.e. off
			changes = append(changes, change{d: d, clear: true})
		}
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
	// A malformed value (a stale env var, a hand-edited row) is dropped rather than handed to the
	// probes, which write it into their own configuration.
	pch, _ := ParseHostPort(effective(snap[KeyProbeCoreHost]))
	sessMaxH := atoiClamp(effective(snap[KeySessionMax]), 12, 1)
	sessIdleMin := atoiClamp(effective(snap[KeySessionIdle]), 0, 0)
	allowed, _ := ParseHostList(effective(snap[KeyAllowedHosts])) // validated on the way in
	alertDelayS := atoiClamp(effective(snap[KeyAlertDelay]), 60, 0)
	trust, _, _ := ParseTrustProxy(effective(snap[KeyTrustProxy])) // validated on the way in
	clock24 := strings.ToLower(effective(snap[KeyTimeFormat])) != "12h"
	autoscale := strings.ToLower(strings.TrimSpace(effective(snap[KeyAutoscale])))
	if autoscale != AutoscaleNextRestart && autoscale != AutoscaleOff {
		autoscale = AutoscaleRestart
	}
	changesKeepD := atoiClamp(effective(snap[KeyChangesKeep]), 365, 30)

	// Apply to the live subsystems (each is independently lock-guarded).
	m.zbx.Configure(zURL, zTok)
	m.limiter.Configure(maxN, time.Duration(winMin)*time.Minute)

	m.mu.Lock()
	m.snap = snap
	m.publicURL = pub
	m.loc = loc
	m.probeCoreHost = pch
	m.sessionMax = time.Duration(sessMaxH) * time.Hour
	m.sessionIdle = time.Duration(sessIdleMin) * time.Minute
	m.allowedHosts = allowed
	m.alertDelay = time.Duration(alertDelayS) * time.Second
	m.trustProxy = trust
	m.clock24h = clock24
	m.autoscale = autoscale
	m.heartbeatURL = effective(snap[KeyHeartbeatURL])
	m.changesKeep = time.Duration(changesKeepD) * 24 * time.Hour
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

// effectiveSecret fetches the actual secret value (env or decrypted DB) - never surfaced to the UI.
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
	switch d.typ {
	case "url":
		return strings.TrimRight(v, "/")
	case "hostlist":
		if list, err := ParseHostList(v); err == nil {
			return strings.Join(list, ", ")
		}
	case "proxylist":
		if _, text, err := ParseTrustProxy(v); err == nil {
			return text // "" (off) is stored as the default
		}
	case "choice":
		return strings.ToLower(v)
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
	case "hostlist":
		if _, err := ParseHostList(v); err != nil {
			return err
		}
	case "host":
		if _, err := ParseHostPort(v); err != nil {
			return fmt.Errorf("%s: %v", d.label, err)
		}
	case "proxylist":
		if _, _, err := ParseTrustProxy(v); err != nil {
			return err
		}
	case "choice":
		for _, o := range choiceOptions[d.key] {
			if strings.EqualFold(v, o) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s", d.label, strings.Join(choiceOptions[d.key], ", "))
	case "tz":
		if _, err := time.LoadLocation(v); err != nil {
			return fmt.Errorf("%q is not a valid IANA timezone", v)
		}
	case "int":
		n, err := strconv.Atoi(v)
		if err != nil || n < d.min {
			return fmt.Errorf("%s must be a whole number ≥ %d", d.label, d.min)
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

// atoiClamp parses s, falling back to def when it isn't a whole number ≥ min.
func atoiClamp(s string, def, min int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= min {
		return n
	}
	return def
}

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// NormalizeHost reduces what an admin might paste (a bare host, host:port, [v6]:port, or a whole URL)
// to a lower-case hostname or IP with no port, or an error if it isn't a valid one.
// hostPortChars is every character a "host" or "host:port" value may contain. The value ends up in
// files that other programs read as configuration (a probe's proxy.env, a Zabbix Server= line),
// so anything else - whitespace, quotes, shell metacharacters, a scheme - is refused outright.
var hostPortChars = regexp.MustCompile(`^[A-Za-z0-9.:_\[\]-]+$`)

// ParseHostPort validates a "host" or "host:port" value (an IP, a name, or a bracketed IPv6
// address, with an optional port) and returns it trimmed. Empty is allowed (it means unset).
func ParseHostPort(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if !hostPortChars.MatchString(v) {
		return "", fmt.Errorf("%q must be a host or host:port, with no spaces or other characters", raw)
	}
	host, port := v, ""
	if h, p, err := net.SplitHostPort(v); err == nil {
		host, port = h, p
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("%q has an invalid port", raw)
		}
	}
	if _, err := NormalizeHost(host); err != nil {
		return "", err
	}
	return v, nil
}

func NormalizeHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(h, "://") {
		u, err := url.Parse(h)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("%q is not a valid host", raw)
		}
		h = u.Hostname()
	} else if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" {
		return "", fmt.Errorf("empty host")
	}
	if net.ParseIP(h) != nil {
		return h, nil
	}
	for _, label := range strings.Split(h, ".") {
		if !hostLabel.MatchString(label) {
			return "", fmt.Errorf("%q is not a valid hostname or IP (wildcards aren't supported)", raw)
		}
	}
	return h, nil
}

// ParseHostList parses the allowed-hosts setting: hosts separated by commas, semicolons or
// whitespace, normalized and de-duplicated. Empty or "*" means the check is off (nil, nil).
func ParseHostList(v string) ([]string, error) {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' })
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		if f == "*" {
			return nil, nil
		}
		h, err := NormalizeHost(f)
		if err != nil {
			return nil, err
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out, nil
}
