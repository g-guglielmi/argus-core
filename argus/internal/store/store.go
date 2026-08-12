// Package store is the embedded SQLite data layer (users, sessions, and later config/CA).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
	CreatedAt    time.Time
}

type Store struct {
	db *sql.DB
}

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
  expires_at INTEGER NOT NULL
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
--   kind 'hide'  scope host|item  — mute in Argus, keep collecting
--   kind 'pause' scope host|item  — expiry bookkeeping for the Zabbix disable (PRTG-style stop)
--   kind 'ack'   scope event      — acknowledged problem (mirrored to Zabbix)
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

-- Small key/value store for app-level flags (e.g. the notifier's one-time baseline marker).
CREATE TABLE IF NOT EXISTS app_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
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

const userColumns = `id,email,name,surname,password_hash,role,totp_secret,totp_enabled,disabled,created_at`

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

func scanUserRow(row rowScanner) (*User, error) {
	var u User
	var created int64
	var totpEnabled, disabled int
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Surname, &u.PasswordHash, &u.Role, &u.TOTPSecret, &totpEnabled, &disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TOTPEnabled = totpEnabled != 0
	u.Disabled = disabled != 0
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (s *Store) scanUser(row *sql.Row) (*User, error) { return scanUserRow(row) }

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUserRow(rows)
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

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// --- MFA (TOTP + recovery codes + login challenges) ---

// SetTOTPSecret stores a pending secret (enrollment); MFA stays disabled until confirmed.
func (s *Store) SetTOTPSecret(ctx context.Context, id int64, secret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?`, secret, id)
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		id, userID, time.Now().Unix(), expires.Unix())
	return err
}

// SessionUser returns the user for a valid, unexpired session id, deleting it if expired.
func (s *Store) SessionUser(ctx context.Context, id string) (*User, error) {
	var userID, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id,expires_at FROM sessions WHERE id=?`, id).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() > expires {
		_ = s.DeleteSession(ctx, id)
		return nil, ErrNotFound
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}
