// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MapPin is where someone moved a device on a site's map: how far from where the automatic layout
// puts it, so a moved switch takes the devices below it along.
type MapPin struct {
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
}

// SiteMap is a probe's network map: off until someone turns it on (an off map is never drawn), who
// did and when, and the devices moved by hand. Turning a map off keeps its layout for next time.
type SiteMap struct {
	Probe string
	On    bool
	OnBy  string
	OnAt  int64
	Pins  map[string]MapPin
}

func scanSiteMap(probe string, on int, by string, at int64, raw string) SiteMap {
	m := SiteMap{Probe: probe, On: on != 0, OnBy: by, OnAt: at, Pins: map[string]MapPin{}}
	_ = json.Unmarshal([]byte(raw), &m.Pins)
	if m.Pins == nil {
		m.Pins = map[string]MapPin{}
	}
	return m
}

// SiteMaps is every probe's map setting, by probe name (a probe with no row has its map off).
func (s *Store) SiteMaps(ctx context.Context) (map[string]SiteMap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT probe, enabled, enabled_by, enabled_at, pins FROM site_maps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SiteMap{}
	for rows.Next() {
		var probe, by, raw string
		var on int
		var at int64
		if err := rows.Scan(&probe, &on, &by, &at, &raw); err != nil {
			return nil, err
		}
		out[probe] = scanSiteMap(probe, on, by, at, raw)
	}
	return out, rows.Err()
}

// SiteMapFor is one probe's map setting (off, with no pins, when it has none).
func (s *Store) SiteMapFor(ctx context.Context, probe string) (SiteMap, error) {
	var by, raw string
	var on int
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT enabled, enabled_by, enabled_at, pins FROM site_maps WHERE probe=?`, probe).Scan(&on, &by, &at, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteMap{Probe: probe, Pins: map[string]MapPin{}}, nil
	}
	if err != nil {
		return SiteMap{}, err
	}
	return scanSiteMap(probe, on, by, at, raw), nil
}

// SetSiteMapOn turns a probe's map on (by whom) or off.
func (s *Store) SetSiteMapOn(ctx context.Context, probe string, on bool, by string) error {
	if !on {
		_, err := s.db.ExecContext(ctx, `UPDATE site_maps SET enabled=0, enabled_by='', enabled_at=0 WHERE probe=?`, probe)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_maps(probe, enabled, enabled_by, enabled_at) VALUES(?,1,?,?)
		ON CONFLICT(probe) DO UPDATE SET enabled=1, enabled_by=excluded.enabled_by, enabled_at=excluded.enabled_at`, probe, by, time.Now().Unix())
	return err
}

// SetSiteMapPins keeps the devices moved by hand on a probe's map (none puts every device back).
func (s *Store) SetSiteMapPins(ctx context.Context, probe string, pins map[string]MapPin) error {
	if pins == nil {
		pins = map[string]MapPin{}
	}
	raw, err := json.Marshal(pins)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO site_maps(probe, pins) VALUES(?,?)
		ON CONFLICT(probe) DO UPDATE SET pins=excluded.pins`, probe, string(raw))
	return err
}
