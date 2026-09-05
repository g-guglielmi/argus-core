package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// UserNotifyChannel is a personal (per-user) alert destination: a user's own Telegram or Discord,
// self-managed from Account settings. It has the same shape and site/severity routing as a global
// NotifyChannel, but is owned by a user and is never type "email". Config holds the type-specific
// keys and is encrypted at rest, exactly like notify_channels.
type UserNotifyChannel struct {
	ID          int64
	UserID      int64
	Type        string // "telegram" | "discord"
	Enabled     bool
	Site        string
	MinSeverity int
	Config      map[string]string
	CreatedAt   time.Time
	// Delivery health, recorded per send (alerts and the Send-test button alike).
	LastSentAt  int64
	LastError   string
	LastErrorAt int64
	SentCount   int64
}

const userChannelColumns = `id,user_id,type,enabled,site,min_severity,config,created_at,last_sent_at,last_error,last_error_at,sent_count`

func (s *Store) scanUserChannel(row rowScanner) (*UserNotifyChannel, error) {
	var c UserNotifyChannel
	var enabled int
	var cfg string
	var created int64
	if err := row.Scan(&c.ID, &c.UserID, &c.Type, &enabled, &c.Site, &c.MinSeverity, &cfg, &created, &c.LastSentAt, &c.LastError, &c.LastErrorAt, &c.SentCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Enabled = enabled != 0
	c.CreatedAt = time.Unix(created, 0)
	c.Config = map[string]string{}
	_ = json.Unmarshal([]byte(s.cipher.Decrypt(cfg)), &c.Config)
	return &c, nil
}

// ListUserNotifyChannels returns one user's personal channels, oldest first.
func (s *Store) ListUserNotifyChannels(ctx context.Context, userID int64) ([]UserNotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userChannelColumns+` FROM user_notify_channels WHERE user_id=? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserNotifyChannel
	for rows.Next() {
		c, err := s.scanUserChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// EnabledUserNotifyChannels returns every enabled personal channel across all users, for the notifier.
func (s *Store) EnabledUserNotifyChannels(ctx context.Context) ([]UserNotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userChannelColumns+` FROM user_notify_channels WHERE enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserNotifyChannel
	for rows.Next() {
		c, err := s.scanUserChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) GetUserNotifyChannel(ctx context.Context, id int64) (*UserNotifyChannel, error) {
	return s.scanUserChannel(s.db.QueryRowContext(ctx, `SELECT `+userChannelColumns+` FROM user_notify_channels WHERE id=?`, id))
}

func (s *Store) CreateUserNotifyChannel(ctx context.Context, c UserNotifyChannel) (int64, error) {
	cfg, _ := json.Marshal(c.Config)
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO user_notify_channels(user_id,type,enabled,site,min_severity,config,created_at) VALUES(?,?,?,?,?,?,?)`,
		c.UserID, c.Type, enabled, c.Site, c.MinSeverity, s.cipher.Encrypt(string(cfg)), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateUserNotifyChannel(ctx context.Context, c UserNotifyChannel) error {
	cfg, _ := json.Marshal(c.Config)
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_notify_channels SET type=?,enabled=?,site=?,min_severity=?,config=? WHERE id=?`,
		c.Type, enabled, c.Site, c.MinSeverity, s.cipher.Encrypt(string(cfg)), c.ID)
	return err
}

func (s *Store) SetUserNotifyChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE user_notify_channels SET enabled=? WHERE id=?`, v, id)
	return err
}

func (s *Store) DeleteUserNotifyChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_notify_channels WHERE id=?`, id)
	return err
}

// RecordUserNotifyDelivery updates a personal channel's delivery health after a send attempt, with the
// same success/last-error semantics as RecordNotifyDelivery for global channels.
func (s *Store) RecordUserNotifyDelivery(ctx context.Context, id int64, sendErr error) error {
	now := time.Now().Unix()
	if sendErr == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE user_notify_channels SET last_sent_at=?, sent_count=sent_count+1 WHERE id=?`, now, id)
		return err
	}
	msg := sendErr.Error()
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE user_notify_channels SET last_error=?, last_error_at=? WHERE id=?`, msg, now, id)
	return err
}

// NotifyUserEmails returns the emails of all active (non-disabled) users, for an email channel that
// delivers to registered users rather than a fixed address.
func (s *Store) NotifyUserEmails(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email FROM users WHERE disabled=0 ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
