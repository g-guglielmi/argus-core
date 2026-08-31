package store

import (
	"context"
	"fmt"
)

// OrderSet is the manual order of one sibling set in the monitoring tree: the ordered children of a
// parent (Scope - the parent group's full path, "" for the top-level roots) of one Kind ("group" or
// "host"). Items holds the child group paths / host ids in their saved position order.
type OrderSet struct {
	Scope string   `json:"scope"`
	Kind  string   `json:"kind"`
	Items []string `json:"items"`
}

// TreeOrder returns every saved sibling ordering, each Items list in saved position order.
func (s *Store) TreeOrder(ctx context.Context) ([]OrderSet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, kind, item FROM tree_order ORDER BY scope, kind, ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]*OrderSet{}
	var keys []string // first-seen order, so the result is deterministic
	for rows.Next() {
		var scope, kind, item string
		if err := rows.Scan(&scope, &kind, &item); err != nil {
			return nil, err
		}
		key := scope + "\x00" + kind
		set := idx[key]
		if set == nil {
			set = &OrderSet{Scope: scope, Kind: kind}
			idx[key] = set
			keys = append(keys, key)
		}
		set.Items = append(set.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]OrderSet, 0, len(keys))
	for _, k := range keys {
		out = append(out, *idx[k])
	}
	return out, nil
}

// SetTreeOrder replaces the saved ordering of one sibling set (Scope+Kind) with items, in the given
// order. An empty items list clears the set, reverting those siblings to alphabetical.
func (s *Store) SetTreeOrder(ctx context.Context, scope, kind string, items []string) error {
	if kind != "group" && kind != "host" {
		return fmt.Errorf("invalid tree order kind %q", kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tree_order WHERE scope=? AND kind=?`, scope, kind); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO tree_order(scope, kind, item, ord) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, item := range items {
		if _, err := stmt.ExecContext(ctx, scope, kind, item, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
