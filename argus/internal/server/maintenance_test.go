// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"path/filepath"
	"testing"
	"time"

	"argus/internal/store"
)

func TestMaintenanceSchedule(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skip("no tz database")
	}
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	sunday := store.MaintenanceWindow{Kind: "weekly", Weekdays: 1 << 0, Minute: 2 * 60, DurationMin: 120, Enabled: true}
	// 2026-10-04 is a Sunday.
	if start, on := occurrenceAt(sunday, at(2026, 10, 4, 3, 0), loc); !on || !start.Equal(at(2026, 10, 4, 2, 0)) {
		t.Fatalf("Sunday 03:00 should be inside the 02:00-04:00 window, got %v %v", start, on)
	}
	if _, on := occurrenceAt(sunday, at(2026, 10, 4, 4, 0), loc); on {
		t.Fatal("04:00 is the end: outside")
	}
	if _, on := occurrenceAt(sunday, at(2026, 10, 5, 3, 0), loc); on {
		t.Fatal("Monday is not in a Sunday window")
	}
	// A window that runs past midnight is found from the next day.
	lateSat := store.MaintenanceWindow{Kind: "weekly", Weekdays: 1 << 6, Minute: 23 * 60, DurationMin: 240, Enabled: true}
	if start, on := occurrenceAt(lateSat, at(2026, 10, 4, 1, 30), loc); !on || !start.Equal(at(2026, 10, 3, 23, 0)) {
		t.Fatalf("Sunday 01:30 is inside Saturday 23:00 + 4 h, got %v %v", start, on)
	}
	// Monthly: the last day, and a day the month doesn't have runs on its last day.
	last := store.MaintenanceWindow{Kind: "monthly", MonthDay: -1, Minute: 60, DurationMin: 60, Enabled: true}
	if _, on := occurrenceAt(last, at(2027, 2, 28, 1, 30), loc); !on {
		t.Fatal("Feb 28 2027 is the last day")
	}
	day31 := store.MaintenanceWindow{Kind: "monthly", MonthDay: 31, Minute: 60, DurationMin: 60, Enabled: true}
	if _, on := occurrenceAt(day31, at(2026, 11, 30, 1, 30), loc); !on {
		t.Fatal("day 31 runs on Nov 30")
	}
	if _, on := occurrenceAt(day31, at(2026, 11, 29, 1, 30), loc); on {
		t.Fatal("not on Nov 29")
	}
	// Once.
	once := store.MaintenanceWindow{Kind: "once", StartAt: at(2026, 10, 10, 20, 0).Unix(), DurationMin: 90, Enabled: true}
	if _, on := occurrenceAt(once, at(2026, 10, 10, 21, 0), loc); !on {
		t.Fatal("inside the one-off")
	}
	if n := nextStart(once, at(2026, 10, 11, 0, 0), loc); !n.IsZero() {
		t.Fatal("a past one-off has no next start")
	}
	// Next start of the Sunday window, seen on a Wednesday.
	if n := nextStart(sunday, at(2026, 10, 7, 12, 0), loc); !n.Equal(at(2026, 10, 11, 2, 0)) {
		t.Fatalf("next Sunday window = %v", n)
	}
	// Across the DST change (2026-10-25 in Europe/Rome), the local start time holds.
	if n := nextStart(sunday, at(2026, 10, 24, 12, 0), loc); n.In(loc).Hour() != 2 {
		t.Fatalf("after DST the window still starts at 02:00 local, got %v", n.In(loc))
	}
	if got := describeWindow(sunday); got != "Sun 02:00 for 2 h" {
		t.Fatalf("describe = %q", got)
	}
}

func TestMaintenanceHitsAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	w := store.MaintenanceWindow{ID: 7, Name: "Backups", Kind: "daily", Minute: 2 * 60, DurationMin: 120, Enabled: true, Sites: []string{"site1"}, HostIDs: []string{"301"}}
	off := w
	off.ID, off.Enabled = 8, false
	off.Sites = []string{"site2"}
	groups := map[string][]string{"101": {"site1/Office"}, "201": {"site2"}, "301": {"site3"}}
	hits := maintenanceHits([]store.MaintenanceWindow{w, off}, groups, time.UTC, now)
	if hits["101"].Name != "Backups" || hits["301"].ID != 7 || hits["101"].Until != now.Add(time.Hour).Unix() {
		t.Fatalf("hits = %+v", hits)
	}
	if _, in := hits["201"]; in {
		t.Fatal("a disabled window holds nothing")
	}
	for _, c := range []struct {
		w    store.MaintenanceWindow
		want string
	}{
		{store.MaintenanceWindow{Name: "", Kind: "daily", DurationMin: 60, Sites: []string{"s"}}, "a name (up to 80 characters) is required"},
		{store.MaintenanceWindow{Name: "x", Kind: "daily", DurationMin: 60}, "pick at least one site or host"},
		{store.MaintenanceWindow{Name: "x", Kind: "daily", DurationMin: 2, Sites: []string{"s"}}, "the duration must be between 5 minutes and 7 days"},
		{store.MaintenanceWindow{Name: "x", Kind: "weekly", DurationMin: 60, Sites: []string{"s"}}, "pick at least one weekday"},
		{store.MaintenanceWindow{Name: "x", Kind: "monthly", MonthDay: 40, DurationMin: 60, Sites: []string{"s"}}, "the day of the month must be 1-31, or the last day"},
		{store.MaintenanceWindow{Name: "x", Kind: "once", DurationMin: 60, Sites: []string{"s"}}, "pick when it starts"},
		{store.MaintenanceWindow{Name: "x", Kind: "daily", DurationMin: 60, HostIDs: []string{"1;2"}}, "invalid host id"},
		{store.MaintenanceWindow{Name: " ok ", Kind: "daily", Minute: 90, DurationMin: 60, Sites: []string{" site1 ", ""}}, ""},
	} {
		w := c.w
		if got := validateWindow(&w); got != c.want {
			t.Errorf("validate(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}

func TestQuietHours(t *testing.T) {
	for _, c := range []struct {
		start, end, m int
		want          bool
	}{
		{22 * 60, 7 * 60, 23 * 60, true}, {22 * 60, 7 * 60, 3 * 60, true}, {22 * 60, 7 * 60, 7 * 60, false},
		{22 * 60, 7 * 60, 12 * 60, false}, {13 * 60, 14 * 60, 13*60 + 30, true}, {-1, -1, 0, false}, {60, 60, 60, false},
	} {
		if got := inQuietHours(c.start, c.end, c.m); got != c.want {
			t.Errorf("inQuietHours(%d,%d,%d) = %v", c.start, c.end, c.m, got)
		}
	}
	quiet := notifyDest{key: "u:1", alerts: true, quietFloor: 4}
	loud := notifyDest{key: "g:1", alerts: true}
	plan := planDeliveries([]notifyDest{quiet, loud}, nil, []string{"site1"}, 2, 1000, 1000, 2000)
	if len(plan) != 1 || plan[0].dest.key != "g:1" {
		t.Fatalf("a warning in quiet hours waits: %+v", plan)
	}
	plan = planDeliveries([]notifyDest{quiet, loud}, nil, []string{"site1"}, 4, 1000, 1000, 2000)
	if len(plan) != 2 {
		t.Fatalf("a high alert gets through quiet hours: %+v", plan)
	}
	// The directory marks who is quiet now, in the Argus timezone.
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	id, _ := st.CreateUser(ctx, store.User{Email: "night@example.com", Role: "viewer", PasswordHash: "x"})
	_ = st.SetUserQuietHours(ctx, id, 22*60, 7*60, 5)
	other, _ := st.CreateUser(ctx, store.User{Email: "day@example.com", Role: "viewer", PasswordHash: "x"})
	dir := loadUserDirectory(ctx, st, time.Date(2026, 10, 4, 23, 30, 0, 0, time.UTC), time.UTC)
	if dir.quiet[id] != 5 || dir.quiet[other] != 0 {
		t.Fatalf("quiet = %v", dir.quiet)
	}
}

func TestMaintenanceWindowScope(t *testing.T) {
	s := &Server{}
	sc := siteScope{sites: []string{"site1/Network"}}
	for _, c := range []struct {
		sites     []string
		see, edit bool
	}{
		{[]string{"site1/Network"}, true, true},
		{[]string{"site1/Network/Core"}, true, true},
		{[]string{"site1"}, true, false},  // wider than theirs: seen, not theirs to change
		{[]string{"site2"}, false, false}, // not theirs at all
		{[]string{"site1/Network", "site2"}, true, false},
	} {
		see, edit := s.windowInScope(t.Context(), sc, store.MaintenanceWindow{Sites: c.sites})
		if see != c.see || edit != c.edit {
			t.Errorf("sites %v: see %v edit %v, want %v %v", c.sites, see, edit, c.see, c.edit)
		}
	}
	if see, edit := s.windowInScope(t.Context(), siteScope{all: true}, store.MaintenanceWindow{Sites: []string{"site9"}}); !see || !edit {
		t.Error("an unscoped user sees and edits everything")
	}
}
