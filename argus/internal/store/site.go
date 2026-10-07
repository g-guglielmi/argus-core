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

// SiteContact is someone to call about a site: "On-site IT, Bob Verdi, +1 555 0101".
type SiteContact struct {
	Role  string `json:"role"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// SiteLine is one of a site's internet lines: who provides it, its circuit or contract, the number to
// call, and the sensor it is tied to (HostID + Key, both optional: Key "" ties it to the whole host).
type SiteLine struct {
	Name     string `json:"name"`     // "WAN 1"
	HostID   string `json:"host_id"`  // the gateway (or modem) it is measured on
	Key      string `json:"key"`      // its sensor there, e.g. unifi.wan.avail[1]
	Provider string `json:"provider"` // "Example Fiber"
	Circuit  string `json:"circuit"`  // circuit ID, contract or SIM number
	Phone    string `json:"phone"`    // the provider's support number
	Note     string `json:"note"`     // "1 Gbps", "LTE backup"
	// The line's speed as contracted, in Mbps (0 = not given; an upload of 0 is the download's): the
	// network map shows how full the line is from it.
	DownMbps float64 `json:"down_mbps,omitempty"`
	UpMbps   float64 `json:"up_mbps,omitempty"`
}

// SiteInfo is what Argus knows about a site (a top-level group) beyond its hosts: where it is, who to
// call, and its internet lines.
type SiteInfo struct {
	Site      string        `json:"site"`
	Address   string        `json:"address"`
	Note      string        `json:"note"`
	Contacts  []SiteContact `json:"contacts"`
	Lines     []SiteLine    `json:"lines"`
	UpdatedAt int64         `json:"updated_at"`
}

// Empty reports whether there is nothing to keep.
func (i SiteInfo) Empty() bool {
	return i.Address == "" && i.Note == "" && len(i.Contacts) == 0 && len(i.Lines) == 0
}

type siteData struct {
	Address  string        `json:"address"`
	Note     string        `json:"note"`
	Contacts []SiteContact `json:"contacts"`
	Lines    []SiteLine    `json:"lines"`
}

func (d siteData) info(site string, at int64) SiteInfo {
	i := SiteInfo{Site: site, Address: d.Address, Note: d.Note, Contacts: d.Contacts, Lines: d.Lines, UpdatedAt: at}
	if i.Contacts == nil {
		i.Contacts = []SiteContact{}
	}
	if i.Lines == nil {
		i.Lines = []SiteLine{}
	}
	return i
}

// SiteInfos is every site's info, by site name.
func (s *Store) SiteInfos(ctx context.Context) (map[string]SiteInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT site, data, updated_at FROM site_info`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SiteInfo{}
	for rows.Next() {
		var site, raw string
		var at int64
		if err := rows.Scan(&site, &raw, &at); err != nil {
			return nil, err
		}
		var d siteData
		_ = json.Unmarshal([]byte(raw), &d)
		out[site] = d.info(site, at)
	}
	return out, rows.Err()
}

// SiteInfoFor is one site's info (an empty one when nothing is kept).
func (s *Store) SiteInfoFor(ctx context.Context, site string) (SiteInfo, error) {
	var raw string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT data, updated_at FROM site_info WHERE site=?`, site).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return siteData{}.info(site, 0), nil
	}
	if err != nil {
		return SiteInfo{}, err
	}
	var d siteData
	_ = json.Unmarshal([]byte(raw), &d)
	return d.info(site, at), nil
}

// SetSiteInfo keeps a site's info; an empty one is removed.
func (s *Store) SetSiteInfo(ctx context.Context, i SiteInfo) error {
	if i.Empty() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM site_info WHERE site=?`, i.Site)
		return err
	}
	raw, err := json.Marshal(siteData{Address: i.Address, Note: i.Note, Contacts: i.Contacts, Lines: i.Lines})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO site_info(site, data, updated_at) VALUES(?,?,?)
		ON CONFLICT(site) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, i.Site, string(raw), time.Now().Unix())
	return err
}

// RenameSiteInfo moves a site's info along when its group is renamed. A site already holding info
// under the new name keeps its own.
func (s *Store) RenameSiteInfo(ctx context.Context, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE OR IGNORE site_info SET site=? WHERE site=?`, to, from)
	return err
}
