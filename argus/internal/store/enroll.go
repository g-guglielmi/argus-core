package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EnrollToken is a probe enrollment credential. The raw token is shown once in the UI; only its
// SHA-256 (TokenHash) is stored. A token is single-use (UsedAt set once redeemed) and time-limited.
type EnrollToken struct {
	ID        int64
	ProxyName string
	Site      string
	CreatedBy *int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

const enrollColumns = `id,proxy_name,site,created_by,created_at,expires_at,used_at`

func scanEnrollToken(row rowScanner) (*EnrollToken, error) {
	var t EnrollToken
	var createdBy sql.NullInt64
	var created, expires int64
	var used sql.NullInt64
	if err := row.Scan(&t.ID, &t.ProxyName, &t.Site, &createdBy, &created, &expires, &used); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if createdBy.Valid {
		t.CreatedBy = &createdBy.Int64
	}
	t.CreatedAt = time.Unix(created, 0)
	t.ExpiresAt = time.Unix(expires, 0)
	if used.Valid {
		u := time.Unix(used.Int64, 0)
		t.UsedAt = &u
	}
	return &t, nil
}

func (s *Store) CreateEnrollToken(ctx context.Context, tokenHash, proxyName, site string, createdBy int64, expires time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO enroll_tokens(token_hash,proxy_name,site,created_by,created_at,expires_at) VALUES(?,?,?,?,?,?)`,
		tokenHash, proxyName, site, createdBy, time.Now().Unix(), expires.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnrollTokenByHash looks up a token by its hash regardless of state; the caller checks expiry/use.
func (s *Store) EnrollTokenByHash(ctx context.Context, tokenHash string) (*EnrollToken, error) {
	return scanEnrollToken(s.db.QueryRowContext(ctx,
		`SELECT `+enrollColumns+` FROM enroll_tokens WHERE token_hash=?`, tokenHash))
}

func (s *Store) ListEnrollTokens(ctx context.Context) ([]EnrollToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+enrollColumns+` FROM enroll_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnrollToken
	for rows.Next() {
		t, err := scanEnrollToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// MarkEnrollTokenUsed stamps a token as redeemed (single-use).
func (s *Store) MarkEnrollTokenUsed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE enroll_tokens SET used_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

// DeleteEnrollToken revokes/removes a token.
func (s *Store) DeleteEnrollToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM enroll_tokens WHERE id=?`, id)
	return err
}
