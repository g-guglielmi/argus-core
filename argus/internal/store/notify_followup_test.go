// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// Deliveries record which channels an alert reached; on a severity change they move to the successor
// (keeping the successor's own row for a channel it already reached), and forgetting an event drops them.
func TestNotifyDeliveries(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertNotifyDelivery(ctx, NotifyDelivery{EventID: "warn", Kind: DeliveryGlobal, ChannelID: 1, Severity: 2, FirstSent: 100, LastSent: 100}))
	must(st.UpsertNotifyDelivery(ctx, NotifyDelivery{EventID: "warn", Kind: DeliveryUser, ChannelID: 1, Severity: 2, FirstSent: 100, LastSent: 400, Reminders: 1}))
	must(st.UpsertNotifyDelivery(ctx, NotifyDelivery{EventID: "high", Kind: DeliveryGlobal, ChannelID: 1, Severity: 4, FirstSent: 500, LastSent: 500}))

	must(st.MoveNotifyDeliveries(ctx, "warn", "high"))
	all, err := st.NotifyDeliveries(ctx)
	must(err)
	if len(all["warn"]) != 0 {
		t.Fatalf("old event kept its deliveries: %+v", all["warn"])
	}
	high := all["high"]
	if len(high) != 2 {
		t.Fatalf("successor deliveries: %+v", high)
	}
	if g := high[DeliveryKey(DeliveryGlobal, 1)]; g.Severity != 4 || g.FirstSent != 500 {
		t.Errorf("the successor's own row must win: %+v", g)
	}
	if u := high[DeliveryKey(DeliveryUser, 1)]; u.Severity != 2 || u.Reminders != 1 || u.LastSent != 400 {
		t.Errorf("the moved row keeps its history: %+v", u)
	}

	must(st.UpsertNotifyState(ctx, NotifyState{EventID: "high", Severity: 4, State: "firing", FirstSeen: 500}))
	must(st.DeleteNotifyState(ctx, "high"))
	all, err = st.NotifyDeliveries(ctx)
	must(err)
	if len(all) != 0 {
		t.Fatalf("deleting the event must drop its deliveries: %+v", all)
	}
}

// The acknowledged-notice flag survives the upsert, and a channel's escalation timing round-trips.
func TestNotifyAckFlagAndChannelTiming(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertNotifyState(ctx, NotifyState{EventID: "e1", Severity: 4, State: "firing", FirstSeen: 1, AckNotified: true}); err != nil {
		t.Fatal(err)
	}
	states, _ := st.NotifyStates(ctx)
	if !states["e1"].AckNotified {
		t.Fatal("ack_notified lost")
	}

	id, err := st.CreateNotifyChannel(ctx, NotifyChannel{Type: "discord", Name: "Managers", Enabled: true, MinSeverity: 4, DelayMin: 30, RepeatMin: 60, Config: map[string]string{"webhook_url": "w"}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetNotifyChannel(ctx, id)
	if c.DelayMin != 30 || c.RepeatMin != 60 {
		t.Fatalf("channel timing: %+v", c)
	}
	c.DelayMin, c.RepeatMin = 0, 15
	if err := st.UpdateNotifyChannel(ctx, *c); err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetNotifyChannel(ctx, id)
	if c.DelayMin != 0 || c.RepeatMin != 15 {
		t.Fatalf("updated timing: %+v", c)
	}

	uid := newTestUser(t, st)
	uc, err := st.CreateUserNotifyChannel(ctx, UserNotifyChannel{UserID: uid, Type: "telegram", Enabled: true, DelayMin: 5, RepeatMin: 30, Config: map[string]string{"bot_token": "t", "chat_id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUserNotifyChannel(ctx, uc)
	if u.DelayMin != 5 || u.RepeatMin != 30 {
		t.Fatalf("personal channel timing: %+v", u)
	}
}

// AckInfo returns who acknowledged an event and the note; an unknown event is ErrNotFound.
func TestAckInfo(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SetSuppression(ctx, "ack", "event", "e1", 7, "on it", nil); err != nil {
		t.Fatal(err)
	}
	by, note, err := st.AckInfo(ctx, "e1")
	if err != nil || by != 7 || note != "on it" {
		t.Fatalf("AckInfo = %d %q %v", by, note, err)
	}
	if _, _, err := st.AckInfo(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("unknown event: %v", err)
	}
}

// Opening the store folds the old High / Disaster floors into "errors only" (3).
func TestAlertLevelMigration(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.CreateNotifyChannel(ctx, NotifyChannel{Type: "email", Name: "Managers", Enabled: true, MinSeverity: 5, RepeatSev: 4, Config: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetNotifyChannel(ctx, id)
	if c.MinSeverity != 3 || c.RepeatSev != 3 {
		t.Fatalf("after migrate: min %d, remind %d", c.MinSeverity, c.RepeatSev)
	}
}

// SyncUnsupported keeps the first time a sensor was seen unsupported, and forgets it once it collects.
func TestSyncUnsupported(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	first, err := st.SyncUnsupported(ctx, []string{"a", "b"})
	if err != nil || len(first) != 2 {
		t.Fatalf("first sync: %v %v", first, err)
	}
	if _, err := st.db.Exec(`UPDATE item_unsupported SET since=100 WHERE item_id='a'`); err != nil {
		t.Fatal(err)
	}
	second, err := st.SyncUnsupported(ctx, []string{"a"})
	if err != nil || second["a"] != 100 || len(second) != 1 {
		t.Fatalf("second sync must keep a's start and drop b: %v %v", second, err)
	}
	all, _ := st.UnsupportedSince(ctx)
	if _, has := all["b"]; has || all["a"] != 100 {
		t.Fatalf("table after sync: %v", all)
	}
}

// A notice is claimed once; releasing it lets it be told again; conditions track since when they hold.
func TestNoticeLedger(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	rel := SentNotice{Key: "argus-release:0.6.0", Kind: NoticeFact}
	if fresh, err := st.ClaimNotice(ctx, rel); err != nil || !fresh {
		t.Fatalf("first claim: %v %v", fresh, err)
	}
	if fresh, _ := st.ClaimNotice(ctx, rel); fresh {
		t.Fatal("a notice was claimed twice")
	}
	facts, _ := st.SentFacts(ctx)
	if len(facts) != 1 || facts[0].Key != rel.Key || facts[0].Cleared != "" {
		t.Fatalf("facts: %+v", facts)
	}
	_ = st.ReleaseNotice(ctx, "argus-release:0.6.0")
	if fresh, _ := st.ClaimNotice(ctx, rel); !fresh {
		t.Fatal("a released notice couldn't be claimed again")
	}
	// A fact with an all-clear keeps what the all-clear needs.
	rep := SentNotice{Key: "probe-report:p1", Kind: NoticeFact, Since: 1700000000, Cleared: "Probe site1 is reporting to Argus again", Host: "Probe site1", Site: "site1", View: "probes"}
	if fresh, err := st.ClaimNotice(ctx, rep); err != nil || !fresh {
		t.Fatalf("claim with all-clear: %v %v", fresh, err)
	}
	facts, _ = st.SentFacts(ctx)
	var got SentNotice
	for _, f := range facts {
		if f.Key == rep.Key {
			got = f
		}
	}
	if got.Since != rep.Since || got.Cleared != rep.Cleared || got.Host != rep.Host || got.Site != rep.Site || got.View != rep.View || got.SentAt == 0 {
		t.Fatalf("all-clear fields not kept: %+v", got)
	}
	_ = st.ReleaseNotice(ctx, rep.Key)
	since, err := st.SyncNoticeConditions(ctx, []string{"core-reboot"})
	if err != nil || since["core-reboot"] == 0 {
		t.Fatalf("conditions: %v %v", since, err)
	}
	if since, _ = st.SyncNoticeConditions(ctx, nil); len(since) != 0 {
		t.Fatalf("ended condition kept: %v", since)
	}
}
