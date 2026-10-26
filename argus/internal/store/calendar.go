// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
)

// A user's calendar feed (server/calendar.go): a secret link to an .ics of the maintenance windows they
// see. The token is looked up by its SHA-256 and kept encrypted so the link can be shown again.

// UserCalendarToken returns a user's feed token (decrypted), or ErrNotFound when they have none.
func (s *Store) UserCalendarToken(ctx context.Context, userID int64) (string, error) {
	var enc string
	err := s.db.QueryRowContext(ctx, `SELECT cal_enc FROM users WHERE id=?`, userID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enc == "") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.cipher.Decrypt(enc), nil
}

// SetUserCalendarToken gives a user a new feed token; the old link stops working at once.
func (s *Store) SetUserCalendarToken(ctx context.Context, userID int64, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET cal_hash=?, cal_enc=? WHERE id=?`, HashStatusToken(token), s.cipher.Encrypt(token), userID)
	return err
}

// UserByCalendarToken finds the user a feed token belongs to.
func (s *Store) UserByCalendarToken(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE cal_hash=? AND cal_hash<>''`, HashStatusToken(token)))
}
