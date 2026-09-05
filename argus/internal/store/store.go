// Package store is the embedded SQLite data layer (users, sessions, and later config/CA).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"argus/internal/secret"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (works with CGO_ENABLED=0)
)

var ErrNotFound = errors.New("not found")

type User struct {
	ID           int64
	Email        string
	Name         string
	Surname      string
	PasswordHash string
	Role         string // admin | helpdesk | viewer
	TOTPSecret   string // base32 TOTP secret ("" when MFA not set up)
	TOTPEnabled  bool   // true once the user has confirmed a code
	Disabled     bool   // true = account suspended; cannot sign in
	Landing      string // preferred landing view on a fresh visit: 'overview' | 'errors'
	Advanced     bool   // show power-user controls in the monitoring tree (per-user opt-in)
	CreatedAt    time.Time
}

type Store struct {
	db     *sql.DB
	cipher *secret.Cipher // nil = passthrough (no at-rest encryption)
}

// SetCipher enables at-rest encryption/decryption of stored secrets. Call once after Open.
func (s *Store) SetCipher(c *secret.Cipher) { s.cipher = c }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite is single-writer; one connection + WAL keeps things simple and lock-free here.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  surname       TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL DEFAULT 0   -- last request time; drives the optional idle timeout
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- One-time recovery codes for MFA; stored hashed (never in the clear).
CREATE TABLE IF NOT EXISTS recovery_codes (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_recovery_user ON recovery_codes(user_id);

-- Short-lived pre-auth challenges issued after a correct password when MFA is on.
CREATE TABLE IF NOT EXISTS mfa_challenges (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

-- Single-use, short-lived self-service password-reset tokens. id is the SHA-256 of the
-- token emailed to the user (never stored in the clear); consumed on use.
CREATE TABLE IF NOT EXISTS password_resets (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_password_resets_user ON password_resets(user_id);

-- Probe enrollment tokens. token_hash is the SHA-256 of the token shown once in the UI; a probe
-- redeems it (single-use) to get its proxy certificate signed and its proxy registered in Zabbix.
CREATE TABLE IF NOT EXISTS enroll_tokens (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  proxy_name TEXT NOT NULL,
  site       TEXT NOT NULL DEFAULT '',
  created_by INTEGER,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER
);

-- Registered WebAuthn credentials (passkeys). credential is the JSON-serialized
-- webauthn.Credential; the raw credential ID is the primary key.
CREATE TABLE IF NOT EXISTS passkeys (
  id           BLOB PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  credential   TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_passkeys_user ON passkeys(user_id);

-- Short-lived WebAuthn ceremony state (registration + discoverable login challenges).
CREATE TABLE IF NOT EXISTS webauthn_sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER,
  data       TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

-- Argus-tracked suppression state with optional expiry (until NULL = indefinite):
--   kind 'hide'  scope host|item  - mute in Argus, keep collecting
--   kind 'pause' scope host|item  - expiry bookkeeping for the Zabbix disable (PRTG-style stop)
--   kind 'ack'   scope event      - acknowledged problem (mirrored to Zabbix)
CREATE TABLE IF NOT EXISTS suppressions (
  kind       TEXT NOT NULL,       -- 'hide' | 'pause' | 'ack'
  scope      TEXT NOT NULL,       -- 'host' | 'item' | 'event'
  target_id  TEXT NOT NULL,
  by_user    INTEGER,
  note       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  until      INTEGER,             -- NULL = indefinite
  PRIMARY KEY (kind, scope, target_id)
);

-- Alert delivery channels (Discord / Telegram / email), managed in the Notifications tab.
-- config is a JSON object of type-specific keys; site is a host-group name ('' = all sites).
CREATE TABLE IF NOT EXISTS notify_channels (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  type       TEXT NOT NULL,       -- 'discord' | 'telegram' | 'email'
  name       TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  site       TEXT NOT NULL DEFAULT '',
  config     TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);

-- Per-problem notifier state machine. state: 'baseline' (present at first startup, never
-- alerted), 'pending' (waiting out the debounce), 'firing' (a problem alert was sent).
CREATE TABLE IF NOT EXISTS notify_events (
  event_id   TEXT PRIMARY KEY,
  host_id    TEXT NOT NULL DEFAULT '',
  host_name  TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  severity   INTEGER NOT NULL DEFAULT 0,
  state      TEXT NOT NULL,
  first_seen INTEGER NOT NULL,
  fired_at   INTEGER
);

-- Per-probe fleet-update agents. A long-lived check-in credential is issued at enrollment
-- (token_hash) so the probe can authenticate version check-ins; version/selfupdate/last_checkin
-- are refreshed on each check-in and power the fleet-update view. Keyed by proxy name.
CREATE TABLE IF NOT EXISTS probe_agents (
  proxy_name   TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  version      TEXT NOT NULL DEFAULT '',   -- last reported image version, e.g. "7.0.29-r1"
  selfupdate   INTEGER NOT NULL DEFAULT 0, -- probe reports whether its self-updater is enabled
  last_checkin INTEGER NOT NULL DEFAULT 0, -- unix seconds of the last check-in (0 = never)
  update_to    TEXT NOT NULL DEFAULT '',   -- pending self-update target tag; handed out once at next check-in
  updater_version   TEXT NOT NULL DEFAULT '', -- version of the argus-updater sidecar managing this probe
  updater_update_to TEXT NOT NULL DEFAULT '', -- pending updater self-update tag; handed out once at next check-in
  bg_user           TEXT NOT NULL DEFAULT '', -- break-glass console username (VM probes report one at first boot)
  bg_secret         TEXT NOT NULL DEFAULT '', -- break-glass password, ENCRYPTED at rest (the existing cipher)
  bg_updated_at     INTEGER NOT NULL DEFAULT 0, -- unix seconds the break-glass credential was last reported
  sec_updates       INTEGER NOT NULL DEFAULT -1, -- pending OS security-update count a probe VM reports (-1 = never reported)
  reboot_required   INTEGER NOT NULL DEFAULT 0,  -- the probe VM's OS flagged /var/run/reboot-required
  os_reported_at    INTEGER NOT NULL DEFAULT 0,  -- unix seconds the OS patch status was last reported
  os_version        TEXT NOT NULL DEFAULT ''     -- the probe VM's OS pretty-name (e.g. "Debian GNU/Linux 13 (trixie)")
);

-- Small key/value store for app-level flags (e.g. the notifier's one-time baseline marker).
CREATE TABLE IF NOT EXISTS app_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- PRTG-style per-sensor display priority (1..5, higher = more important). Argus-only: it reorders the
-- overview and status lists and touches nothing in Zabbix. Only real overrides live here - a sensor
-- absent from this table takes DefaultItemPriority.
CREATE TABLE IF NOT EXISTS item_priority (
  item_id    TEXT PRIMARY KEY,
  priority   INTEGER NOT NULL,   -- 1..5
  by_user    INTEGER,            -- who last set it
  updated_at INTEGER NOT NULL
);

-- Per-proxy SNMP defaults (PRTG-style inheritance). community + the v3 passphrases are encrypted at
-- rest. Hosts inheriting from a proxy take these values; changing them propagates to inheriting hosts.
CREATE TABLE IF NOT EXISTS snmp_defaults (
  proxy_id     TEXT PRIMARY KEY,
  version      INTEGER NOT NULL,
  community    TEXT,             -- encrypted
  bulk         INTEGER NOT NULL DEFAULT 1,
  sec_name     TEXT,
  sec_level    INTEGER NOT NULL DEFAULT 0,
  auth_proto   INTEGER NOT NULL DEFAULT 0,
  auth_pass    TEXT,             -- encrypted
  priv_proto   INTEGER NOT NULL DEFAULT 0,
  priv_pass    TEXT,             -- encrypted
  context_name TEXT,
  updated_at   INTEGER NOT NULL
);

-- Which host SNMP interfaces are managed by their proxy's default (inherit=1) vs overridden (0). An
-- interface absent here is treated as an override and never touched by propagation.
CREATE TABLE IF NOT EXISTS snmp_iface (
  interface_id TEXT PRIMARY KEY,
  inherit      INTEGER NOT NULL
);

-- Manual sibling ordering for the monitoring tree. scope is the parent group's full path ('' for the
-- top-level roots); kind is 'group' or 'host'; item is the child group's full path or the host id;
-- ord is its 0-based position among its siblings. A sibling set with no rows sorts alphabetically.
CREATE TABLE IF NOT EXISTS tree_order (
  scope TEXT NOT NULL,
  kind  TEXT NOT NULL,
  item  TEXT NOT NULL,
  ord   INTEGER NOT NULL,
  PRIMARY KEY (scope, kind, item)
);

-- Group paths hidden from the monitoring tree (Argus-local curation - the group still exists in Zabbix,
-- it's just tucked out of view, subtree and all). Used mainly to hide the stock Zabbix groups that can't
-- be deleted because a host prototype references them.
CREATE TABLE IF NOT EXISTS tree_hidden (
  path TEXT PRIMARY KEY
);
`); err != nil {
		return err
	}

	// Additive column migrations for databases created before these features existed.
	if err := s.ensureColumn("users", "totp_secret TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "totp_enabled INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "webauthn_handle BLOB"); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "disabled INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "landing TEXT NOT NULL DEFAULT 'overview'"); err != nil {
		return err
	}
	if err := s.ensureColumn("users", "advanced INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("sessions", "last_seen INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "update_to TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// The argus-updater sidecar managing this probe: its reported version + a pending self-update tag
	// (the updater recreates itself onto it via the probe-recreate primitive).
	if err := s.ensureColumn("probe_agents", "updater_version TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "updater_update_to TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Break-glass console credential a probe VM generates + reports at first boot (bg_secret encrypted).
	if err := s.ensureColumn("probe_agents", "bg_user TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "bg_secret TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "bg_updated_at INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// OS patch status a probe VM's host-side reporter posts (security-update count + reboot-required),
	// so the fleet view can show which sites carry pending CVEs / need a reboot. See DESIGN §14c.
	if err := s.ensureColumn("probe_agents", "sec_updates INTEGER NOT NULL DEFAULT -1"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "reboot_required INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "os_reported_at INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("probe_agents", "os_version TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("notify_events", "item_id TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Per-channel minimum severity (Zabbix 0..5); default 2 = Warning, matching the old global floor.
	if err := s.ensureColumn("notify_channels", "min_severity INTEGER NOT NULL DEFAULT 2"); err != nil {
		return err
	}
	return nil
}

// ensureColumn adds a column, treating "already exists" as success so migrate() is idempotent.
func (s *Store) ensureColumn(table, ddl string) error {
	_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, ddl))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

// --- users ---

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, u User) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users(email,name,surname,password_hash,role,created_at) VALUES(?,?,?,?,?,?)`,
		u.Email, u.Name, u.Surname, u.PasswordHash, u.Role, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const userColumns = `id,email,name,surname,password_hash,role,totp_secret,totp_enabled,disabled,landing,advanced,created_at`

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email=?`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id=?`, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUserRow(row rowScanner) (*User, error) {
	var u User
	var created int64
	var totpEnabled, disabled, advanced int
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Surname, &u.PasswordHash, &u.Role, &u.TOTPSecret, &totpEnabled, &disabled, &u.Landing, &advanced, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPSecret = s.cipher.Decrypt(u.TOTPSecret)
	u.TOTPEnabled = totpEnabled != 0
	u.Disabled = disabled != 0
	u.Advanced = advanced != 0
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (s *Store) scanUser(row *sql.Row) (*User, error) { return s.scanUserRow(row) }

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := s.scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) UpdateUserProfile(ctx context.Context, id int64, email, name, surname, role string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email=?,name=?,surname=?,role=? WHERE id=?`, email, name, surname, role, id)
	return err
}

// SetUserDisabled suspends or re-enables an account (a disabled user cannot sign in).
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET disabled=? WHERE id=?`, v, id)
	return err
}

// CountAdmins returns the number of enabled admin accounts (for last-admin guardrails).
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

func (s *Store) UpdatePassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, hash, id)
	return err
}

// UpdateUserLanding stores the user's preferred landing view ('overview' | 'errors').
func (s *Store) UpdateUserLanding(ctx context.Context, id int64, landing string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET landing=? WHERE id=?`, landing, id)
	return err
}

// UpdateUserAdvanced stores the user's advanced-mode opt-in (power-user tree controls).
func (s *Store) UpdateUserAdvanced(ctx context.Context, id int64, advanced bool) error {
	v := 0
	if advanced {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET advanced=? WHERE id=?`, v, id)
	return err
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// --- at-rest secret encryption ---

// GetOrCreateSigningSecret returns the persistent alert-link HMAC secret (decrypted), generating
// one on first use and migrating a pre-existing plaintext value to encrypted form.
func (s *Store) GetOrCreateSigningSecret(ctx context.Context) (string, error) {
	raw, ok, err := s.MetaGet(ctx, "signing_secret")
	if err != nil {
		return "", err
	}
	if ok && raw != "" {
		if s.cipher.Enabled() && !secret.IsEncrypted(raw) {
			_ = s.MetaSet(ctx, "signing_secret", s.cipher.Encrypt(raw))
		}
		return s.cipher.Decrypt(raw), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	plain := hex.EncodeToString(buf)
	if err := s.MetaSet(ctx, "signing_secret", s.cipher.Encrypt(plain)); err != nil {
		return "", err
	}
	return plain, nil
}

// EncryptPlaintextSecrets re-stores any not-yet-encrypted channel configs and TOTP seeds in
// encrypted form. Idempotent; a no-op when encryption is disabled. Returns how many rows changed.
func (s *Store) EncryptPlaintextSecrets(ctx context.Context) (int, error) {
	if !s.cipher.Enabled() {
		return 0, nil
	}
	n := 0
	migrate := func(sel, upd string) error {
		type row struct {
			id  int64
			val string
		}
		var todo []row
		rows, err := s.db.QueryContext(ctx, sel)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.val); err != nil {
				rows.Close()
				return err
			}
			if !secret.IsEncrypted(x.val) {
				todo = append(todo, x)
			}
		}
		rows.Close()
		for _, x := range todo {
			if _, err := s.db.ExecContext(ctx, upd, s.cipher.Encrypt(x.val), x.id); err != nil {
				return err
			}
			n++
		}
		return nil
	}
	if err := migrate(`SELECT id, config FROM notify_channels`, `UPDATE notify_channels SET config=? WHERE id=?`); err != nil {
		return n, err
	}
	if err := migrate(`SELECT id, totp_secret FROM users WHERE totp_secret != ''`, `UPDATE users SET totp_secret=? WHERE id=?`); err != nil {
		return n, err
	}
	return n, nil
}

// --- MFA (TOTP + recovery codes + login challenges) ---

// SetTOTPSecret stores a pending secret (enrollment); MFA stays disabled until confirmed.
func (s *Store) SetTOTPSecret(ctx context.Context, id int64, totpSecret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?`, s.cipher.Encrypt(totpSecret), id)
	return err
}

// EnableTOTP flips MFA on after the user confirms a valid code.
func (s *Store) EnableTOTP(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_enabled=1 WHERE id=?`, id)
	return err
}

// DisableTOTP clears the secret, disables MFA, and discards any recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceRecoveryCodes swaps the user's recovery codes for a fresh set of hashes.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes(user_id,code_hash) VALUES(?,?)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ConsumeRecoveryCode marks a matching unused code used and reports whether one was found.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE recovery_codes SET used_at=? WHERE user_id=? AND code_hash=? AND used_at IS NULL`,
		time.Now().Unix(), userID, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountUnusedRecoveryCodes reports how many recovery codes remain for a user.
func (s *Store) CountUnusedRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_codes WHERE user_id=? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Store) CreateMFAChallenge(ctx context.Context, id string, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mfa_challenges(id,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		id, userID, time.Now().Unix(), expires.Unix())
	return err
}

// MFAChallengeUserID returns the user for a valid, unexpired challenge, deleting it if expired.
func (s *Store) MFAChallengeUserID(ctx context.Context, id string) (int64, error) {
	var userID, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id,expires_at FROM mfa_challenges WHERE id=?`, id).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if time.Now().Unix() > expires {
		_ = s.DeleteMFAChallenge(ctx, id)
		return 0, ErrNotFound
	}
	return userID, nil
}

func (s *Store) DeleteMFAChallenge(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE id=?`, id)
	return err
}

// --- password resets ---

func (s *Store) CreatePasswordReset(ctx context.Context, id string, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO password_resets(id,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		id, userID, time.Now().Unix(), expires.Unix())
	return err
}

// PasswordResetUserID returns the user for a valid, unexpired reset token, deleting it if expired.
func (s *Store) PasswordResetUserID(ctx context.Context, id string) (int64, error) {
	var userID, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id,expires_at FROM password_resets WHERE id=?`, id).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if time.Now().Unix() > expires {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM password_resets WHERE id=?`, id)
		return 0, ErrNotFound
	}
	return userID, nil
}

// DeleteUserPasswordResets clears every outstanding reset token for a user (called after a
// successful reset, so a second link can't be reused).
func (s *Store) DeleteUserPasswordResets(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id=?`, userID)
	return err
}

// DeleteUserSessions signs a user out everywhere (called after a password reset).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// --- suppressions (hide / pause-expiry / ack) with optional expiry ---

// Suppression identifies a scope+target (used when sweeping expired entries).
type Suppression struct {
	Scope    string
	TargetID string
}

// SetSuppression upserts a suppression; until is the expiry unix time (nil = indefinite).
func (s *Store) SetSuppression(ctx context.Context, kind, scope, targetID string, byUser int64, note string, until *int64) error {
	var u any
	if until != nil {
		u = *until
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO suppressions(kind,scope,target_id,by_user,note,created_at,until) VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(kind,scope,target_id) DO UPDATE SET by_user=excluded.by_user, note=excluded.note, created_at=excluded.created_at, until=excluded.until`,
		kind, scope, targetID, byUser, note, time.Now().Unix(), u)
	return err
}

func (s *Store) ClearSuppression(ctx context.Context, kind, scope, targetID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM suppressions WHERE kind=? AND scope=? AND target_id=?`, kind, scope, targetID)
	return err
}

// ActiveSuppressionMap returns non-expired target ids mapped to their expiry (nil = indefinite).
func (s *Store) ActiveSuppressionMap(ctx context.Context, kind, scope string) (map[string]*int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT target_id, until FROM suppressions WHERE kind=? AND scope=? AND (until IS NULL OR until > ?)`,
		kind, scope, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*int64{}
	for rows.Next() {
		var id string
		var until sql.NullInt64
		if err := rows.Scan(&id, &until); err != nil {
			return nil, err
		}
		if until.Valid {
			v := until.Int64
			out[id] = &v
		} else {
			out[id] = nil
		}
	}
	return out, rows.Err()
}

// --- per-sensor display priority (PRTG-style, Argus-only) ---

// DefaultItemPriority is the priority a sensor has until someone sets one (1..5, middle of the range).
const DefaultItemPriority = 3

// ItemPriorities returns every explicitly-set sensor priority (item_id -> 1..5). Sensors absent from
// the map take DefaultItemPriority.
func (s *Store) ItemPriorities(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, priority FROM item_priority`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var p int
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// SetItemPriority upserts a sensor's priority. Setting it back to the default deletes the row, so the
// table only ever holds genuine overrides.
func (s *Store) SetItemPriority(ctx context.Context, itemID string, priority int, byUser int64) error {
	if priority == DefaultItemPriority {
		_, err := s.db.ExecContext(ctx, `DELETE FROM item_priority WHERE item_id=?`, itemID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO item_priority(item_id,priority,by_user,updated_at) VALUES(?,?,?,?)
		 ON CONFLICT(item_id) DO UPDATE SET priority=excluded.priority, by_user=excluded.by_user, updated_at=excluded.updated_at`,
		itemID, priority, byUser, time.Now().Unix())
	return err
}

// ExpiredPauses returns pause suppressions whose expiry has passed (for the re-enable sweeper).
func (s *Store) ExpiredPauses(ctx context.Context) ([]Suppression, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, target_id FROM suppressions WHERE kind='pause' AND until IS NOT NULL AND until <= ?`,
		time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Suppression
	for rows.Next() {
		var sp Suppression
		if err := rows.Scan(&sp.Scope, &sp.TargetID); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// DeleteExpiredNonPause removes expired hide/ack rows (pause rows are cleared by the sweeper
// after re-enabling in Zabbix).
func (s *Store) DeleteExpiredNonPause(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM suppressions WHERE kind!='pause' AND until IS NOT NULL AND until <= ?`, time.Now().Unix())
	return err
}

// --- passkeys / WebAuthn ---

// Passkey is the display-facing metadata for a registered credential.
type Passkey struct {
	ID         []byte
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// EnsureWebAuthnHandle returns the user's stable WebAuthn user handle, generating and
// persisting a random one on first use.
func (s *Store) EnsureWebAuthnHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	err := s.db.QueryRowContext(ctx, `SELECT webauthn_handle FROM users WHERE id=?`, userID).Scan(&h)
	if err != nil {
		return nil, err
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 16)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET webauthn_handle=? WHERE id=?`, h, userID); err != nil {
		return nil, err
	}
	return h, nil
}

// UserByWebAuthnHandle finds a user by their WebAuthn handle (used for discoverable login).
func (s *Store) UserByWebAuthnHandle(ctx context.Context, handle []byte) (*User, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE webauthn_handle=?`, handle).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

// AddPasskey stores a newly registered credential (credential is JSON).
func (s *Store) AddPasskey(ctx context.Context, id []byte, userID int64, name, credential string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO passkeys(id,user_id,name,credential,created_at) VALUES(?,?,?,?,?)`,
		id, userID, name, credential, time.Now().Unix())
	return err
}

// PasskeyCredentials returns the raw JSON credential blobs for a user (for the WebAuthn lib).
func (s *Store) PasskeyCredentials(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT credential FROM passkeys WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListPasskeys returns display metadata for a user's passkeys, newest first.
func (s *Store) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,created_at,last_used_at FROM passkeys WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &created, &last); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(created, 0)
		if last.Valid {
			t := time.Unix(last.Int64, 0)
			p.LastUsedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePasskeyCredential rewrites a credential blob (e.g. after a sign-count bump) and marks it used.
func (s *Store) UpdatePasskeyCredential(ctx context.Context, id []byte, credential string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET credential=?, last_used_at=? WHERE id=?`, credential, time.Now().Unix(), id)
	return err
}

// DeletePasskey removes one credential owned by the user.
func (s *Store) DeletePasskey(ctx context.Context, id []byte, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id=? AND user_id=?`, id, userID)
	return err
}

// DeleteAllPasskeys removes every credential for a user (admin reset).
func (s *Store) DeleteAllPasskeys(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id=?`, userID)
	return err
}

// CountPasskeys reports how many passkeys a user has registered.
func (s *Store) CountPasskeys(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

func (s *Store) SaveWebAuthnSession(ctx context.Context, id string, userID *int64, data string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webauthn_sessions(id,user_id,data,expires_at) VALUES(?,?,?,?)`,
		id, userID, data, expires.Unix())
	return err
}

// WebAuthnSession returns the stored ceremony data for a valid, unexpired id, deleting it if expired.
func (s *Store) WebAuthnSession(ctx context.Context, id string) (userID *int64, data string, err error) {
	var uid sql.NullInt64
	var expires int64
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id,data,expires_at FROM webauthn_sessions WHERE id=?`, id).Scan(&uid, &data, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if time.Now().Unix() > expires {
		_ = s.DeleteWebAuthnSession(ctx, id)
		return nil, "", ErrNotFound
	}
	if uid.Valid {
		userID = &uid.Int64
	}
	return userID, data, nil
}

func (s *Store) DeleteWebAuthnSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webauthn_sessions WHERE id=?`, id)
	return err
}

// --- sessions ---

func (s *Store) CreateSession(ctx context.Context, id string, userID int64, expires time.Time) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id,user_id,created_at,expires_at,last_seen) VALUES(?,?,?,?,?)`,
		id, userID, now, expires.Unix(), now)
	return err
}

// touchThreshold throttles the last_seen write so a busy session doesn't cause a DB write on
// every request (SQLite is single-writer). Idle resolution is coarse enough that a minute of
// slack is irrelevant.
const touchThreshold = 60 // seconds

// SessionUserTouch validates a session against its absolute expiry, a live max-lifetime cap, and an
// optional idle timeout, refreshes last_seen (throttled), and returns the user. A session is deleted
// and treated as not-found once any bound is crossed. idle <= 0 disables the idle check.
//
// The effective expiry is min(stored expires_at, created_at + maxLifetime): the stored expiry is
// frozen at login, but maxLifetime is read live each request, so lowering it in Settings shortens
// existing sessions immediately (raising it never retroactively extends them - the stored expiry
// still caps). maxLifetime <= 0 disables the live cap.
func (s *Store) SessionUserTouch(ctx context.Context, id string, idle, maxLifetime time.Duration, now time.Time) (*User, error) {
	var userID, created, expires, lastSeen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id,created_at,expires_at,last_seen FROM sessions WHERE id=?`, id).Scan(&userID, &created, &expires, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	nowUnix := now.Unix()
	if nowUnix > expires {
		_ = s.DeleteSession(ctx, id)
		return nil, ErrNotFound
	}
	if maxLifetime > 0 && nowUnix > created+int64(maxLifetime.Seconds()) {
		_ = s.DeleteSession(ctx, id)
		return nil, ErrNotFound
	}
	if idle > 0 && lastSeen > 0 && nowUnix-lastSeen > int64(idle.Seconds()) {
		_ = s.DeleteSession(ctx, id)
		return nil, ErrNotFound
	}
	if nowUnix-lastSeen >= touchThreshold {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen=? WHERE id=?`, nowUnix, id)
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}
