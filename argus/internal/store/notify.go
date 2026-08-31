package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// NotifyChannel is a stored alert delivery target. Config holds type-specific keys.
type NotifyChannel struct {
	ID          int64
	Type        string
	Name        string
	Enabled     bool
	Site        string
	MinSeverity int // Zabbix severity floor (0..5); a problem below this doesn't reach this channel
	Config      map[string]string
	CreatedAt   time.Time
}

// NotifyState is one row of the notifier state machine (keyed by Zabbix event id).
type NotifyState struct {
	EventID   string
	HostID    string
	ItemID    string
	HostName  string
	Name      string
	Severity  int
	State     string // 'baseline' | 'pending' | 'firing'
	FirstSeen int64
	FiredAt   *int64
}

// --- channels ---

func (s *Store) scanChannel(row rowScanner) (*NotifyChannel, error) {
	var c NotifyChannel
	var enabled int
	var cfg string
	var created int64
	if err := row.Scan(&c.ID, &c.Type, &c.Name, &enabled, &c.Site, &c.MinSeverity, &cfg, &created); err != nil {
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

const channelColumns = `id,type,name,enabled,site,min_severity,config,created_at`

func (s *Store) ListNotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelColumns+` FROM notify_channels ORDER BY site, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotifyChannel
	for rows.Next() {
		c, err := s.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// EnabledNotifyChannels returns only channels that are switched on (for the notifier).
func (s *Store) EnabledNotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	all, err := s.ListNotifyChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) GetNotifyChannel(ctx context.Context, id int64) (*NotifyChannel, error) {
	return s.scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM notify_channels WHERE id=?`, id))
}

func (s *Store) CreateNotifyChannel(ctx context.Context, c NotifyChannel) (int64, error) {
	cfg, _ := json.Marshal(c.Config)
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_channels(type,name,enabled,site,min_severity,config,created_at) VALUES(?,?,?,?,?,?,?)`,
		c.Type, c.Name, enabled, c.Site, c.MinSeverity, s.cipher.Encrypt(string(cfg)), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateNotifyChannel(ctx context.Context, c NotifyChannel) error {
	cfg, _ := json.Marshal(c.Config)
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE notify_channels SET type=?,name=?,enabled=?,site=?,min_severity=?,config=? WHERE id=?`,
		c.Type, c.Name, enabled, c.Site, c.MinSeverity, s.cipher.Encrypt(string(cfg)), c.ID)
	return err
}

func (s *Store) SetNotifyChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET enabled=? WHERE id=?`, v, id)
	return err
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_channels WHERE id=?`, id)
	return err
}

// --- notifier state machine ---

// NotifyStates returns every tracked event keyed by event id.
func (s *Store) NotifyStates(ctx context.Context) (map[string]NotifyState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id,host_id,item_id,host_name,name,severity,state,first_seen,fired_at FROM notify_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NotifyState{}
	for rows.Next() {
		var st NotifyState
		var fired sql.NullInt64
		if err := rows.Scan(&st.EventID, &st.HostID, &st.ItemID, &st.HostName, &st.Name, &st.Severity, &st.State, &st.FirstSeen, &fired); err != nil {
			return nil, err
		}
		if fired.Valid {
			v := fired.Int64
			st.FiredAt = &v
		}
		out[st.EventID] = st
	}
	return out, rows.Err()
}

// UpsertNotifyState inserts or replaces a state row.
func (s *Store) UpsertNotifyState(ctx context.Context, st NotifyState) error {
	var fired any
	if st.FiredAt != nil {
		fired = *st.FiredAt
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_events(event_id,host_id,item_id,host_name,name,severity,state,first_seen,fired_at)
		 VALUES(?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(event_id) DO UPDATE SET
		   host_id=excluded.host_id, item_id=excluded.item_id, host_name=excluded.host_name, name=excluded.name,
		   severity=excluded.severity, state=excluded.state, fired_at=excluded.fired_at`,
		st.EventID, st.HostID, st.ItemID, st.HostName, st.Name, st.Severity, st.State, st.FirstSeen, fired)
	return err
}

func (s *Store) DeleteNotifyState(ctx context.Context, eventID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notify_events WHERE event_id=?`, eventID)
	return err
}

// --- app_meta ---

func (s *Store) MetaGet(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) MetaSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO app_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

func (s *Store) MetaDelete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_meta WHERE key=?`, key)
	return err
}

// MetaGetSecret reads an app_meta value stored encrypted (transparently decrypting it).
func (s *Store) MetaGetSecret(ctx context.Context, key string) (string, bool, error) {
	raw, ok, err := s.MetaGet(ctx, key)
	if err != nil || !ok {
		return "", ok, err
	}
	return s.cipher.Decrypt(raw), true, nil
}

// MetaSetSecret stores an app_meta value encrypted at rest (channel-credential style).
func (s *Store) MetaSetSecret(ctx context.Context, key, plain string) error {
	return s.MetaSet(ctx, key, s.cipher.Encrypt(plain))
}
