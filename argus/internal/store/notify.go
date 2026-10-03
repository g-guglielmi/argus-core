// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// NotifyChannel is a stored alert delivery target. Config holds type-specific keys.
type NotifyChannel struct {
	ID          int64
	Type        string
	Name        string
	Enabled     bool
	Sites       []string // host-group names this channel serves; empty = all sites
	Tags        []string // only hosts with one of these tags; empty = every host
	MinSeverity int      // Zabbix severity floor (0..5); a problem below this doesn't reach this channel
	DelayMin    int      // escalation: minutes open + unacknowledged before this channel is told (0 = at once)
	RepeatMin   int      // reminders: minutes between repeats while open + unacknowledged (0 = none)
	RepeatSev   int      // reminders only for problems at or above this severity (2..5)
	Alerts      bool     // carries problem alerts
	Notices     bool     // carries Argus's system notices
	Config      map[string]string
	CreatedAt   time.Time
	// Delivery health, recorded per send (alerts and the Send-test button alike) and shown on the
	// Notifications cards: when the channel last delivered, and the last failure with its reason.
	LastSentAt  int64
	LastError   string
	LastErrorAt int64
	SentCount   int64
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
	// IncidentStart is when the incident behind this alert began (0 = unknown; use FirstSeen).
	IncidentStart int64
	// AckNotified is set once the channels that got the alert were told it was acknowledged.
	AckNotified bool
	// HeldAt is the last time the alert was held back because its master sensor was down (0 = never).
	HeldAt int64
}

// --- channels ---

// encodeSites serializes a channel's site scope for the `site` column: a JSON array of host-group
// names, or "" for the empty set (= all sites). decodeSites reads it back, tolerating the legacy
// single-value form (a bare group name written before channels could serve multiple sites).
func encodeSites(sites []string) string {
	clean := make([]string, 0, len(sites))
	for _, s := range sites {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	b, _ := json.Marshal(clean)
	return string(b)
}

func decodeSites(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		var out []string
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
	}
	return []string{v}
}

func (s *Store) scanChannel(row rowScanner) (*NotifyChannel, error) {
	var c NotifyChannel
	var enabled, alerts, notices int
	var cfg string
	var site, tags string
	var created int64
	if err := row.Scan(&c.ID, &c.Type, &c.Name, &enabled, &site, &c.MinSeverity, &cfg, &created, &c.LastSentAt, &c.LastError, &c.LastErrorAt, &c.SentCount, &c.DelayMin, &c.RepeatMin, &c.RepeatSev, &alerts, &notices, &tags); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Enabled = enabled != 0
	c.Alerts, c.Notices = alerts != 0, notices != 0
	c.Sites = decodeSites(site)
	c.Tags = decodeSites(tags)
	c.CreatedAt = time.Unix(created, 0)
	c.Config = map[string]string{}
	_ = json.Unmarshal([]byte(s.cipher.Decrypt(cfg)), &c.Config)
	return &c, nil
}

const channelColumns = `id,type,name,enabled,site,min_severity,config,created_at,last_sent_at,last_error,last_error_at,sent_count,delay_min,repeat_min,repeat_min_severity,alerts,system_notices,tags`

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
		`INSERT INTO notify_channels(type,name,enabled,site,min_severity,config,created_at,delay_min,repeat_min,repeat_min_severity,alerts,system_notices,tags) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Type, c.Name, enabled, encodeSites(c.Sites), c.MinSeverity, s.cipher.Encrypt(string(cfg)), time.Now().Unix(), c.DelayMin, c.RepeatMin, c.RepeatSev, boolInt(c.Alerts), boolInt(c.Notices), encodeSites(c.Tags))
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
		`UPDATE notify_channels SET type=?,name=?,enabled=?,site=?,min_severity=?,config=?,delay_min=?,repeat_min=?,repeat_min_severity=?,alerts=?,system_notices=?,tags=? WHERE id=?`,
		c.Type, c.Name, enabled, encodeSites(c.Sites), c.MinSeverity, s.cipher.Encrypt(string(cfg)), c.DelayMin, c.RepeatMin, c.RepeatSev, boolInt(c.Alerts), boolInt(c.Notices), encodeSites(c.Tags), c.ID)
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

// RecordNotifyDelivery updates a channel's delivery health after a send attempt: a success stamps
// last_sent_at and bumps sent_count; a failure records the error text and time (the previous success
// is kept, so the card can say "last sent 2h ago, last failure 5m ago"). The error is truncated so a
// long SMTP transcript can't bloat the row.
func (s *Store) RecordNotifyDelivery(ctx context.Context, id int64, sendErr error) error {
	now := time.Now().Unix()
	if sendErr == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET last_sent_at=?, sent_count=sent_count+1 WHERE id=?`, now, id)
		return err
	}
	msg := sendErr.Error()
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET last_error=?, last_error_at=? WHERE id=?`, msg, now, id)
	return err
}

// boolInt stores a bool as SQLite's 0/1.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- notifier state machine ---

// NotifyStates returns every tracked event keyed by event id.
func (s *Store) NotifyStates(ctx context.Context) (map[string]NotifyState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id,host_id,item_id,host_name,name,severity,state,first_seen,fired_at,incident_start,ack_notified,held_at FROM notify_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NotifyState{}
	for rows.Next() {
		var st NotifyState
		var fired sql.NullInt64
		var ackN int
		if err := rows.Scan(&st.EventID, &st.HostID, &st.ItemID, &st.HostName, &st.Name, &st.Severity, &st.State, &st.FirstSeen, &fired, &st.IncidentStart, &ackN, &st.HeldAt); err != nil {
			return nil, err
		}
		st.AckNotified = ackN != 0
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
	ackN := 0
	if st.AckNotified {
		ackN = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_events(event_id,host_id,item_id,host_name,name,severity,state,first_seen,fired_at,incident_start,ack_notified,held_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(event_id) DO UPDATE SET
		   host_id=excluded.host_id, item_id=excluded.item_id, host_name=excluded.host_name, name=excluded.name,
		   severity=excluded.severity, state=excluded.state, fired_at=excluded.fired_at,
		   incident_start=excluded.incident_start, ack_notified=excluded.ack_notified, held_at=excluded.held_at`,
		st.EventID, st.HostID, st.ItemID, st.HostName, st.Name, st.Severity, st.State, st.FirstSeen, fired, st.IncidentStart, ackN, st.HeldAt)
	return err
}

// DeleteNotifyState forgets an event, along with the record of which channels it reached.
func (s *Store) DeleteNotifyState(ctx context.Context, eventID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_deliveries WHERE event_id=?`, eventID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_events WHERE event_id=?`, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- per-channel deliveries (escalation, reminders, acknowledged + recovery routing) ---

// Delivery kinds: which channel table a delivery's channel_id refers to.
const (
	DeliveryGlobal = "g" // notify_channels
	DeliveryUser   = "u" // user_notify_channels
)

// NotifyDelivery records that an alert reached one channel.
type NotifyDelivery struct {
	EventID   string
	Kind      string // DeliveryGlobal | DeliveryUser
	ChannelID int64
	Severity  int   // the level this channel was last alerted at
	FirstSent int64 // unix s
	LastSent  int64 // unix s: the alert or its latest reminder
	Reminders int
}

// DeliveryKey identifies a channel across both channel tables.
func DeliveryKey(kind string, channelID int64) string {
	return kind + ":" + strconv.FormatInt(channelID, 10)
}

// NotifyDeliveries returns every recorded delivery, grouped by event id and keyed by DeliveryKey.
func (s *Store) NotifyDeliveries(ctx context.Context) (map[string]map[string]NotifyDelivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id,kind,channel_id,severity,first_sent,last_sent,reminders FROM notify_deliveries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]NotifyDelivery{}
	for rows.Next() {
		var d NotifyDelivery
		if err := rows.Scan(&d.EventID, &d.Kind, &d.ChannelID, &d.Severity, &d.FirstSent, &d.LastSent, &d.Reminders); err != nil {
			return nil, err
		}
		if out[d.EventID] == nil {
			out[d.EventID] = map[string]NotifyDelivery{}
		}
		out[d.EventID][DeliveryKey(d.Kind, d.ChannelID)] = d
	}
	return out, rows.Err()
}

// UpsertNotifyDelivery inserts or replaces one delivery row.
func (s *Store) UpsertNotifyDelivery(ctx context.Context, d NotifyDelivery) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notify_deliveries(event_id,kind,channel_id,severity,first_sent,last_sent,reminders) VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(event_id,kind,channel_id) DO UPDATE SET
		   severity=excluded.severity, first_sent=excluded.first_sent, last_sent=excluded.last_sent, reminders=excluded.reminders`,
		d.EventID, d.Kind, d.ChannelID, d.Severity, d.FirstSent, d.LastSent, d.Reminders)
	return err
}

// MoveNotifyDeliveries hands an alert's deliveries over to the problem that took over on the same
// sensor at another severity, so its recovery still reaches every channel that heard of the incident.
// A channel the successor already reached keeps the successor's row.
func (s *Store) MoveNotifyDeliveries(ctx context.Context, fromEvent, toEvent string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO notify_deliveries(event_id,kind,channel_id,severity,first_sent,last_sent,reminders)
		 SELECT ?,kind,channel_id,severity,first_sent,last_sent,reminders FROM notify_deliveries WHERE event_id=?`,
		toEvent, fromEvent); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_deliveries WHERE event_id=?`, fromEvent); err != nil {
		return err
	}
	return tx.Commit()
}

// AckInfo returns who acknowledged an event and their note (byUser 0 = the signed alert link).
func (s *Store) AckInfo(ctx context.Context, eventID string) (byUser int64, note string, err error) {
	var by sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT by_user, note FROM suppressions WHERE kind='ack' AND scope='event' AND target_id=?`, eventID).Scan(&by, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return by.Int64, note, err
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
