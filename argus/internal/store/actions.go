// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Action is a fix someone runs from a sensor in trouble (server/actions.go, DESIGN 7r). Type is what
// it does (service, container, command, netscaler, webhook), Via how it reaches the host (ssh, agent,
// or "probe" for the probe itself). Scope says where it applies: one sensor ("item", ItemID), every
// sensor of a kind on hosts of a class ("kind", ClassID + KeyKind), one host ("host", HostID) or
// every host of a class ("class", ClassID). Config is the type's own settings as JSON; ScriptID the
// Zabbix script that runs it.
type Action struct {
	ID          int64
	Name        string
	Type        string
	Via         string
	Scope       string
	ItemID      string
	HostID      string
	ClassID     string
	KeyKind     string
	Label       string
	Config      string
	CooldownMin int
	ScriptID    string
	ByName      string
	CreatedAt   int64
	UpdatedAt   int64
}

const actionColumns = `id, name, type, via, scope, item_id, host_id, class_id, key_kind, label, config, cooldown_min, script_id, by_name, created_at, updated_at`

func scanAction(sc interface{ Scan(...any) error }) (Action, error) {
	var a Action
	err := sc.Scan(&a.ID, &a.Name, &a.Type, &a.Via, &a.Scope, &a.ItemID, &a.HostID, &a.ClassID, &a.KeyKind, &a.Label, &a.Config, &a.CooldownMin, &a.ScriptID, &a.ByName, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// Actions returns every action, by name.
func (s *Store) Actions(ctx context.Context) ([]Action, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+actionColumns+` FROM actions ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ErrNoAction is an action that isn't there.
var ErrNoAction = errors.New("no such action")

// ActionByID returns one action.
func (s *Store) ActionByID(ctx context.Context, id int64) (Action, error) {
	a, err := scanAction(s.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM actions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNoAction
	}
	return a, err
}

// SaveAction adds an action (ID 0; answers its new id) or rewrites one.
func (s *Store) SaveAction(ctx context.Context, a Action) (int64, error) {
	now := time.Now().Unix()
	if a.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO actions(name, type, via, scope, item_id, host_id, class_id, key_kind, label, config, cooldown_min, script_id, by_name, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.Name, a.Type, a.Via, a.Scope, a.ItemID, a.HostID, a.ClassID, a.KeyKind, a.Label, a.Config, a.CooldownMin, a.ScriptID, a.ByName, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE actions SET name=?, type=?, via=?, scope=?, item_id=?, host_id=?, class_id=?, key_kind=?, label=?, config=?, cooldown_min=?, script_id=?, by_name=?, updated_at=? WHERE id=?`,
		a.Name, a.Type, a.Via, a.Scope, a.ItemID, a.HostID, a.ClassID, a.KeyKind, a.Label, a.Config, a.CooldownMin, a.ScriptID, a.ByName, now, a.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNoAction
	}
	return a.ID, nil
}

// SetActionScript records the Zabbix script an action runs as.
func (s *Store) SetActionScript(ctx context.Context, id int64, scriptID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE actions SET script_id=? WHERE id=?`, scriptID, id)
	return err
}

// DeleteAction removes an action (its runs stay until they age out).
func (s *Store) DeleteAction(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM actions WHERE id=?`, id)
	return err
}

// ActionRun is one run of an action.
type ActionRun struct {
	ID         int64  `json:"id"`
	ActionID   int64  `json:"action_id"`
	ActionName string `json:"action"`
	HostID     string `json:"host_id"`
	HostName   string `json:"host"`
	ItemID     string `json:"item_id,omitempty"`
	Sensor     string `json:"sensor,omitempty"`
	At         int64  `json:"at"`
	OK         bool   `json:"ok"`
	Why        string `json:"why,omitempty"`
	ByName     string `json:"by"`
	TookMS     int64  `json:"took_ms"`
}

// AddActionRun records a run.
func (s *Store) AddActionRun(ctx context.Context, r ActionRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO action_runs(action_id, action_name, host_id, host_name, item_id, sensor, at, ok, why, by_name, took_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.ActionID, r.ActionName, r.HostID, r.HostName, r.ItemID, r.Sensor, r.At, boolInt(r.OK), r.Why, r.ByName, r.TookMS)
	return err
}

// LastActionRuns answers each action's latest run (on any host), and each action's latest run on
// each host (keyed by ActionHostKey).
func (s *Store) LastActionRuns(ctx context.Context) (map[int64]ActionRun, map[string]ActionRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.action_id, r.action_name, r.host_id, r.host_name, r.item_id, r.sensor, r.at, r.ok, r.why, r.by_name, r.took_ms
		FROM action_runs r JOIN (SELECT MAX(id) AS id FROM action_runs GROUP BY action_id, host_id) m ON m.id = r.id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byAction, byHost := map[int64]ActionRun{}, map[string]ActionRun{}
	for rows.Next() {
		var r ActionRun
		var ok int
		if err := rows.Scan(&r.ID, &r.ActionID, &r.ActionName, &r.HostID, &r.HostName, &r.ItemID, &r.Sensor, &r.At, &ok, &r.Why, &r.ByName, &r.TookMS); err != nil {
			return nil, nil, err
		}
		r.OK = ok != 0
		if cur, seen := byAction[r.ActionID]; !seen || r.ID > cur.ID {
			byAction[r.ActionID] = r
		}
		byHost[ActionHostKey(r.ActionID, r.HostID)] = r
	}
	return byAction, byHost, rows.Err()
}

// ActionHostKey is the key LastActionRuns files an action's run on a host under.
func ActionHostKey(actionID int64, hostID string) string {
	return strconv.FormatInt(actionID, 10) + "|" + hostID
}

// PruneActionRuns drops the runs older than before.
func (s *Store) PruneActionRuns(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM action_runs WHERE at < ?`, before)
	return err
}
