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

// ClaimNotice records a notice as sent and reports whether it is new, i.e. whether the caller should
// send it now. A key already in the ledger returns false.
func (s *Store) ClaimNotice(ctx context.Context, key, kind string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO notices_sent(key, kind, sent_at) VALUES(?, ?, ?)`, key, kind, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseNotice forgets one notice, so it can be claimed (and sent) again.
func (s *Store) ReleaseNotice(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices_sent WHERE key=?`, key)
	return err
}

// SentFacts returns the keys of the fact notices currently in the ledger.
func (s *Store) SentFacts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM notices_sent WHERE kind=?`, NoticeFact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PruneNoticeEvents drops one-off notices older than a year (long enough that a still-present
// outcome, like a finished core update awaiting dismissal, is never told twice).
func (s *Store) PruneNoticeEvents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices_sent WHERE kind=? AND sent_at<?`, NoticeEvent, time.Now().Add(-noticeEventKeep).Unix())
	return err
}
