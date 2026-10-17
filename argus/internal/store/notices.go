// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"time"
)

// Notice kinds for the sent-notices ledger.
const (
	NoticeFact  = "fact"  // a condition that holds for a while; forgotten once it ends
	NoticeEvent = "event" // a one-off; kept for a year
)

const noticeEventKeep = 365 * 24 * time.Hour

// SentNotice is one row of the sent-notices ledger. A fact keeps what its all-clear needs once the
// condition ends: since when it held, the words to say it cleared (none = no all-clear), and where
// it belonged (host, site, view), since the condition itself is gone by then.
type SentNotice struct {
	Key     string
	Kind    string
	SentAt  int64
	Since   int64  // when the condition started holding (facts)
	Cleared string // the all-clear's title, "" when the notice has none
	Host    string
	Site    string
	View    string
}

// ClaimNotice records a notice as sent and reports whether it is new, i.e. whether the caller should
// send it now. A key already in the ledger returns false.
func (s *Store) ClaimNotice(ctx context.Context, n SentNotice) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO notices_sent(key, kind, sent_at, since, cleared, host, site, view) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Key, n.Kind, time.Now().Unix(), n.Since, n.Cleared, n.Host, n.Site, n.View)
	if err != nil {
		return false, err
	}
	c, err := res.RowsAffected()
	return c == 1, err
}

// ReleaseNotice forgets one notice, so it can be claimed (and sent) again.
func (s *Store) ReleaseNotice(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices_sent WHERE key=?`, key)
	return err
}

// SentFacts returns the fact notices currently in the ledger.
func (s *Store) SentFacts(ctx context.Context) ([]SentNotice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, kind, sent_at, since, cleared, host, site, view FROM notices_sent WHERE kind=?`, NoticeFact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SentNotice
	for rows.Next() {
		var n SentNotice
		if err := rows.Scan(&n.Key, &n.Kind, &n.SentAt, &n.Since, &n.Cleared, &n.Host, &n.Site, &n.View); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PruneNoticeEvents drops one-off notices older than a year (long enough that a still-present
// outcome, like a finished core update awaiting dismissal, is never told twice).
func (s *Store) PruneNoticeEvents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices_sent WHERE kind=? AND sent_at<?`, NoticeEvent, time.Now().Add(-noticeEventKeep).Unix())
	return err
}
