// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// APIToken is a personal API token: it acts as its user, with their role and sites, narrowed to its
// scope. Only the token's SHA-256 is kept; the token itself is shown once, when it is made.
type APIToken struct {
	ID         int64
	UserID     int64
	Name       string
	Scope      string // read | ack | maint | all
	Hint       string // its last 4 characters, to tell tokens apart
	CreatedAt  int64
	ExpiresAt  int64 // 0 = never
	LastUsedAt int64
	LastIP     string
}

// TokenScopes are the scopes a token can have, narrowest first.
var TokenScopes = []string{"read", "ack", "maint", "all"}

const tokenColumns = `id, user_id, name, scope, hint, created_at, expires_at, last_used_at, last_ip`

func scanToken(row rowScanner) (APIToken, error) {
	var t APIToken
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.Hint, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.LastIP)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// CreateAPIToken keeps a new token by its hash.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, hash string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens (user_id, name, scope, hint, hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Name, t.Scope, t.Hint, hash, time.Now().Unix(), t.ExpiresAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// APITokens lists a user's tokens, newest first.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// APITokenCounts is how many tokens each user has (the Users page).
func (s *Store) APITokenCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COUNT(*) FROM api_tokens GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// APITokenUser resolves a token's hash to the token and its user: ErrNotFound for an unknown or
// expired token, or a user who can't sign in. Its last use is recorded at most once a minute.
func (s *Store) APITokenUser(ctx context.Context, hash, ip string, now time.Time) (*User, APIToken, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE hash = ?`, hash))
	if err != nil {
		return nil, t, err
	}
	if t.ExpiresAt > 0 && now.Unix() > t.ExpiresAt {
		return nil, t, ErrNotFound
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil {
		return nil, t, err
	}
	if u.Disabled {
		return nil, t, ErrNotFound
	}
	if now.Unix()-t.LastUsedAt >= 60 || t.LastIP != ip {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?, last_ip = ? WHERE id = ?`, now.Unix(), ip, t.ID)
	}
	return u, t, nil
}

// APITokenByID is one token of a user's.
func (s *Store) APITokenByID(ctx context.Context, id, userID int64) (APIToken, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID))
}

// DeleteAPIToken revokes one of a user's tokens.
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserAPITokens revokes every token of a user (an admin's "Revoke API tokens").
func (s *Store) DeleteUserAPITokens(ctx context.Context, userID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
