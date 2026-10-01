// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PushSensor is a job that reports to Argus when it runs, at its own secret URL: a backup, a cron job,
// a scheduled task. Argus keeps its last run; the host's "Argus Push" template reads the host's push
// sensors back once a minute through its proxy, so each one is an ordinary Zabbix sensor.
type PushSensor struct {
	ID         int64
	HostID     string
	Name       string
	LateSecs   int64 // a warning once this long passes without a run
	MissedSecs int64 // an error once this long passes without a run
	CreatedAt  int64
	CreatedBy  string
	LastAt     int64 // unix s of the last run, 0 = none yet
	LastOK     bool  // the last run reported success (true before the first run)
	LastMsg    string
	Runs       int64
	HasLink    bool // the URL can be copied again (its token is kept encrypted)
}

const pushColumns = `id,host_id,name,late_secs,missed_secs,created_at,created_by,last_at,last_ok,last_msg,runs,token_enc<>''`

func scanPush(row rowScanner) (*PushSensor, error) {
	var p PushSensor
	if err := row.Scan(&p.ID, &p.HostID, &p.Name, &p.LateSecs, &p.MissedSecs, &p.CreatedAt, &p.CreatedBy, &p.LastAt, &p.LastOK, &p.LastMsg, &p.Runs, &p.HasLink); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// ListPushSensors returns a host's push sensors ("" = every host's), oldest first.
func (s *Store) ListPushSensors(ctx context.Context, hostID string) ([]PushSensor, error) {
	q, args := `SELECT `+pushColumns+` FROM push_sensors ORDER BY id`, []any{}
	if hostID != "" {
		q, args = `SELECT `+pushColumns+` FROM push_sensors WHERE host_id=? ORDER BY id`, []any{hostID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSensor
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPushSensor returns one push sensor by id.
func (s *Store) GetPushSensor(ctx context.Context, id int64) (*PushSensor, error) {
	return scanPush(s.db.QueryRowContext(ctx, `SELECT `+pushColumns+` FROM push_sensors WHERE id=?`, id))
}

// CreatePushSensor stores a new push sensor with its token (hashed, and encrypted) and returns its id.
func (s *Store) CreatePushSensor(ctx context.Context, p PushSensor, token string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO push_sensors(host_id,name,token_hash,token_enc,late_secs,missed_secs,created_at,created_by) VALUES(?,?,?,?,?,?,?,?)`,
		p.HostID, p.Name, HashStatusToken(token), s.cipher.Encrypt(token), p.LateSecs, p.MissedSecs, time.Now().Unix(), p.CreatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePushSensor changes a push sensor's name and times (not its URL).
func (s *Store) UpdatePushSensor(ctx context.Context, p PushSensor) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_sensors SET name=?,late_secs=?,missed_secs=? WHERE id=?`, p.Name, p.LateSecs, p.MissedSecs, p.ID)
	return err
}

// RotatePushToken gives a push sensor a new URL: the old one stops working at once.
func (s *Store) RotatePushToken(ctx context.Context, id int64, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_sensors SET token_hash=?, token_enc=? WHERE id=?`, HashStatusToken(token), s.cipher.Encrypt(token), id)
	return err
}

// PushToken returns a push sensor's token (decrypted), or ErrNotFound when it isn't kept.
func (s *Store) PushToken(ctx context.Context, id int64) (string, error) {
	var enc string
	err := s.db.QueryRowContext(ctx, `SELECT token_enc FROM push_sensors WHERE id=?`, id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enc == "") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.cipher.Decrypt(enc), nil
}

// DeletePushSensor removes a push sensor; its URL stops working at once.
func (s *Store) DeletePushSensor(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_sensors WHERE id=?`, id)
	return err
}

// RecordPush stores a run reported at a push sensor's URL and returns the sensor (ErrNotFound for an
// unknown token).
func (s *Store) RecordPush(ctx context.Context, token string, ok bool, msg string, at time.Time) (*PushSensor, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE push_sensors SET last_at=?, last_ok=?, last_msg=?, runs=runs+1 WHERE token_hash=?`,
		at.Unix(), ok, msg, HashStatusToken(token))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return scanPush(s.db.QueryRowContext(ctx, `SELECT `+pushColumns+` FROM push_sensors WHERE token_hash=?`, HashStatusToken(token)))
}

// PushHostKeyHash returns the SHA-256 of the key a host's template reads its push sensors with.
func (s *Store) PushHostKeyHash(ctx context.Context, hostID string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM push_hosts WHERE host_id=?`, hostID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// SetPushHostKey stores (the SHA-256 of) a host's read key, replacing the one it had.
func (s *Store) SetPushHostKey(ctx context.Context, hostID, key string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_hosts(host_id,key_hash) VALUES(?,?) ON CONFLICT(host_id) DO UPDATE SET key_hash=excluded.key_hash`,
		hostID, HashStatusToken(key))
	return err
}

// DeletePushHost forgets a host's read key (its last push sensor is gone).
func (s *Store) DeletePushHost(ctx context.Context, hostID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_hosts WHERE host_id=?`, hostID)
	return err
}
