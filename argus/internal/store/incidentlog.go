// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import "context"

// IncidentLogEntry is one thing that happened to an incident (server/timeline.go): it opened, a channel
// was alerted (or couldn't be), escalated to, reminded, it was held or waited for maintenance, someone
// acknowledged it or left a note, the channels were told, it ended. Kept a year.
type IncidentLogEntry struct {
	ID      int64  `json:"-"`
	EventID string `json:"-"`
	At      int64  `json:"at"`
	Kind    string `json:"kind"`
	Who     string `json:"who,omitempty"`
	Channel string `json:"channel,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// AddIncidentLog records one entry.
func (s *Store) AddIncidentLog(ctx context.Context, e IncidentLogEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO incident_log(event_id, at, kind, who, channel, detail) VALUES(?,?,?,?,?,?)`,
		e.EventID, e.At, e.Kind, e.Who, e.Channel, e.Detail)
	return err
}

// IncidentLog returns an incident's entries in the order they happened.
func (s *Store) IncidentLog(ctx context.Context, eventID string) ([]IncidentLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, event_id, at, kind, who, channel, detail FROM incident_log WHERE event_id=? ORDER BY at, id LIMIT 500`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncidentLogEntry{}
	for rows.Next() {
		var e IncidentLogEntry
		if err := rows.Scan(&e.ID, &e.EventID, &e.At, &e.Kind, &e.Who, &e.Channel, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// IncidentLogged returns which of these incidents have a timeline.
func (s *Store) IncidentLogged(ctx context.Context, eventIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(eventIDs); start += 500 {
		end := min(start+500, len(eventIDs))
		args := make([]any, 0, end-start)
		ph := ""
		for i, id := range eventIDs[start:end] {
			if i > 0 {
				ph += ","
			}
			ph += "?"
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT event_id FROM incident_log WHERE event_id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
	}
	return out, nil
}

// PruneIncidentLog drops entries older than before (unix s).
func (s *Store) PruneIncidentLog(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM incident_log WHERE at < ?`, before)
	return err
}
