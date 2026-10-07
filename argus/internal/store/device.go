// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// HostOwnFacts are the facts about a host only a person knows: its asset tag and where it is. The
// rest (model, serial, firmware, MAC) is read from the device.
type HostOwnFacts struct {
	AssetTag string
	Location string
}

// HostFacts returns every host's own facts.
func (s *Store) HostFacts(ctx context.Context) (map[string]HostOwnFacts, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, asset_tag, location FROM host_facts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HostOwnFacts{}
	for rows.Next() {
		var id string
		var f HostOwnFacts
		if err := rows.Scan(&id, &f.AssetTag, &f.Location); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

// SetHostFacts stores a host's own facts (both empty drops the row).
func (s *Store) SetHostFacts(ctx context.Context, hostID string, f HostOwnFacts) error {
	if f.AssetTag == "" && f.Location == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM host_facts WHERE host_id = ?`, hostID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_facts (host_id, asset_tag, location, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET asset_tag = excluded.asset_tag, location = excluded.location, updated_at = excluded.updated_at`,
		hostID, f.AssetTag, f.Location, time.Now().Unix())
	return err
}

// Link is a button on a host: a label and the address it opens.
type Link struct {
	ID    int64
	Label string
	URL   string
}

// LinkTemplate is a link every host of some classes gets, its address filled in from each host.
type LinkTemplate struct {
	ID      int64
	Label   string
	URL     string   // with {ip}, {name}, {host}, {mac}, {group} and {macro:NAME}
	Classes []string // device class ids; empty = every class
}

// LinkTemplates returns the link templates, by label.
func (s *Store) LinkTemplates(ctx context.Context) ([]LinkTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, url, classes FROM link_templates ORDER BY label COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkTemplate{}
	for rows.Next() {
		var t LinkTemplate
		var classes string
		if err := rows.Scan(&t.ID, &t.Label, &t.URL, &classes); err != nil {
			return nil, err
		}
		t.Classes = decodeSites(classes)
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveLinkTemplate creates (ID 0) or updates a link template and returns its id.
func (s *Store) SaveLinkTemplate(ctx context.Context, t LinkTemplate) (int64, error) {
	if t.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO link_templates (label, url, classes, created_at) VALUES (?, ?, ?, ?)`, t.Label, t.URL, encodeSites(t.Classes), time.Now().Unix())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE link_templates SET label = ?, url = ?, classes = ? WHERE id = ?`, t.Label, t.URL, encodeSites(t.Classes), t.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return t.ID, nil
}

// DeleteLinkTemplate removes a link template.
func (s *Store) DeleteLinkTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM link_templates WHERE id = ?`, id)
	return err
}

// seedLinkTemplates gives a new install the one link most devices with a web page want. Once: an
// admin who deletes it doesn't get it back.
func (s *Store) seedLinkTemplates(ctx context.Context) error {
	var done string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = 'links_seeded'`).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	web := []string{"unifi-gateway", "unifi-console", "ugreen-agent", "unraid-snmp", "home-assistant", "adguard"}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO link_templates (label, url, classes, created_at) VALUES ('Web UI', 'https://{ip}', ?, ?)`, encodeSites(web), time.Now().Unix()); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_meta (key, value) VALUES ('links_seeded', '1')`)
	return err
}

// HostLinks returns a host's own links.
func (s *Store) HostLinks(ctx context.Context, hostID string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, url FROM host_links WHERE host_id = ? ORDER BY id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Label, &l.URL); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetHostLinks replaces a host's own links.
func (s *Store) SetHostLinks(ctx context.Context, hostID string, links []Link) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM host_links WHERE host_id = ?`, hostID); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `INSERT INTO host_links (host_id, label, url) VALUES (?, ?, ?)`, hostID, l.Label, l.URL); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// JournalEntry is a lasting note on a host: what happened, what was done, a ticket number. Unlike a
// sensor note it stays as long as the host.
type JournalEntry struct {
	ID        int64
	HostID    string
	Kind      string // info | warning | problem
	Text      string
	ByUser    int64
	ByName    string
	CreatedAt int64
}

// HostJournal returns a host's journal, newest first.
func (s *Store) HostJournal(ctx context.Context, hostID string) ([]JournalEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, host_id, kind, text, by_user, by_name, created_at FROM host_journal WHERE host_id = ? ORDER BY created_at DESC, id DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JournalEntry{}
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.HostID, &e.Kind, &e.Text, &e.ByUser, &e.ByName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddJournalEntry writes an entry and returns it with its id.
func (s *Store) AddJournalEntry(ctx context.Context, e JournalEntry) (JournalEntry, error) {
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO host_journal (host_id, kind, text, by_user, by_name, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.HostID, e.Kind, strings.TrimSpace(e.Text), e.ByUser, e.ByName, e.CreatedAt)
	if err != nil {
		return e, err
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// JournalEntryByID reads one entry.
func (s *Store) JournalEntryByID(ctx context.Context, id int64) (JournalEntry, error) {
	var e JournalEntry
	err := s.db.QueryRowContext(ctx, `SELECT id, host_id, kind, text, by_user, by_name, created_at FROM host_journal WHERE id = ?`, id).
		Scan(&e.ID, &e.HostID, &e.Kind, &e.Text, &e.ByUser, &e.ByName, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// DeleteJournalEntry removes an entry.
func (s *Store) DeleteJournalEntry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM host_journal WHERE id = ?`, id)
	return err
}

// JournalCounts returns how many entries each host's journal has.
func (s *Store) JournalCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, COUNT(*) FROM host_journal GROUP BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DiscoveredMACs returns the MAC discovery saw for each host it adopted (the newest scan's).
func (s *Store) DiscoveredMACs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, mac FROM discovery_results WHERE host_id != '' AND mac != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, mac string
		if err := rows.Scan(&id, &mac); err != nil {
			return nil, err
		}
		out[id] = mac
	}
	return out, rows.Err()
}

// HostUpstream is how a host's upstream device is set: the controller's answer (auto, the default),
// one chosen by hand (manual), or none; and the controller's last answer, kept to log its changes.
type HostUpstream struct {
	Mode       string // auto | manual | none
	ManualHost string
	AutoHost   string
	AutoPort   string
	AutoAt     int64  // when the automatic answer was first stored; 0 = never
	AutoSource string // who gave it: "controller" (the UniFi controller) or a hypervisor ("xcpng")
}

// HostUpstreams returns every host's upstream setting (a host without a row is auto with no answer).
func (s *Store) HostUpstreams(ctx context.Context) (map[string]HostUpstream, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, mode, manual_host, auto_host, auto_port, auto_at, auto_source FROM host_upstream`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HostUpstream{}
	for rows.Next() {
		var id string
		var u HostUpstream
		if err := rows.Scan(&id, &u.Mode, &u.ManualHost, &u.AutoHost, &u.AutoPort, &u.AutoAt, &u.AutoSource); err != nil {
			return nil, err
		}
		if u.AutoSource == "" {
			u.AutoSource = "controller"
		}
		out[id] = u
	}
	return out, rows.Err()
}

// SetUpstreamMode stores how a host's upstream is set (manualHost only for "manual").
func (s *Store) SetUpstreamMode(ctx context.Context, hostID, mode, manualHost string) error {
	if mode != "manual" {
		manualHost = ""
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_upstream (host_id, mode, manual_host) VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET mode = excluded.mode, manual_host = excluded.manual_host`, hostID, mode, manualHost)
	return err
}

// SetAutoUpstream stores the automatic answer for a host, and who gave it (source).
func (s *Store) SetAutoUpstream(ctx context.Context, hostID, host, port, source string, at int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_upstream (host_id, auto_host, auto_port, auto_at, auto_source) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET auto_host = excluded.auto_host, auto_port = excluded.auto_port, auto_at = excluded.auto_at,
		auto_source = excluded.auto_source`, hostID, host, port, at, source)
	return err
}
