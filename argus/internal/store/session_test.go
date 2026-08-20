package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a throwaway on-disk store (SQLite modernc driver, one conn).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestUser(t *testing.T, st *Store) int64 {
	t.Helper()
	id, err := st.CreateUser(context.Background(), User{
		Email: "u@example.com", Name: "U", Surname: "Ser", PasswordHash: "x", Role: "admin",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return id
}

// TestSessionMaxLifetimeLiveCap verifies that the absolute max lifetime is enforced live against
// created_at each request - not frozen at login - so lowering it in Settings shortens existing
// sessions even when the stored expires_at is still far in the future.
func TestSessionMaxLifetimeLiveCap(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)

	// A session whose STORED expiry is 7 days out (as if created when the max was set high).
	start := time.Now()
	if err := st.CreateSession(ctx, "sess-cap", uid, start.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Within a 12h live cap -> still valid.
	if _, err := st.SessionUserTouch(ctx, "sess-cap", 0, 12*time.Hour, start.Add(11*time.Hour)); err != nil {
		t.Fatalf("within cap: want valid, got %v", err)
	}

	// Past the 12h live cap, even though the stored expiry is days away -> gone.
	if _, err := st.SessionUserTouch(ctx, "sess-cap", 0, 12*time.Hour, start.Add(13*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("past cap: want ErrNotFound, got %v", err)
	}
	// And it was deleted, not merely rejected.
	if _, err := st.SessionUserTouch(ctx, "sess-cap", 0, 0, start.Add(14*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
}

// TestSessionMaxLifetimeDisabled verifies max<=0 disables the live cap (only the stored expiry gates).
func TestSessionMaxLifetimeDisabled(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	uid := newTestUser(t, st)

	start := time.Now()
	if err := st.CreateSession(ctx, "sess-nocap", uid, start.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// 13h elapsed but no live cap and stored expiry days away -> still valid.
	if _, err := st.SessionUserTouch(ctx, "sess-nocap", 0, 0, start.Add(13*time.Hour)); err != nil {
		t.Fatalf("no cap: want valid, got %v", err)
	}
	// Past the stored expiry -> gone regardless.
	if _, err := st.SessionUserTouch(ctx, "sess-nocap", 0, 0, start.Add(8*24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("past stored expiry: want ErrNotFound, got %v", err)
	}
}
