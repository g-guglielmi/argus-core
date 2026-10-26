// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
	"time"
)

// The change log keeps one entry per action with the hosts it touched: a host's own list finds the
// bulk action that named it, the filters narrow by text and kind, and old entries prune.
func TestChangeLog(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	err := st.AddChanges(ctx, []Change{
		{At: 100, ActorKind: "user", ActorID: 1, Actor: "Alice Rossi", Category: "states", Action: "Paused 3 hosts", Object: "ap-lobby, ap-office, cam-entrance",
			Detail: "for 2 h", Reason: "replacing a power supply", HostIDs: []string{"21", "22", "23", "22"}},
		{At: 200, ActorKind: "user", ActorID: 1, Actor: "Alice Rossi", Category: "thresholds", Action: "Changed a default threshold", Object: "Argus UniFi Switch",
			Diff: []ChangeDiff{{Field: "Temperature warning", Old: "70 °C", New: "65 °C"}}},
		{At: 300, ActorKind: "argus", Actor: "Argus", Category: "hosts", Action: "Upstream device", Object: "ap-lobby", HostIDs: []string{"21"},
			Diff: []ChangeDiff{{Field: "Upstream", Old: "", New: "sw-core port 24"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	all, err := st.ListChanges(ctx, ChangeQuery{})
	if err != nil || len(all) != 3 || all[0].At != 300 || all[2].Action != "Paused 3 hosts" {
		t.Fatalf("all: %+v %v", all, err)
	}
	if got := all[2].HostIDs; len(got) != 3 || got[0] != "21" || got[2] != "23" {
		t.Fatalf("bulk entry hosts (deduped, in order): %v", got)
	}
	if d := all[1].Diff; len(d) != 1 || d[0].Old != "70 °C" || d[0].New != "65 °C" {
		t.Fatalf("diff: %+v", d)
	}

	host, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{"21"}})
	if len(host) != 2 || host[0].ActorKind != "argus" || host[1].Reason != "replacing a power supply" {
		t.Fatalf("host 21's changes: %+v", host)
	}
	if none, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{}}); len(none) != 0 {
		t.Fatalf("an empty host filter matched %d", len(none))
	}
	if txt, _ := st.ListChanges(ctx, ChangeQuery{Text: "TEMPERATURE"}); len(txt) != 1 || txt[0].Category != "thresholds" {
		t.Fatalf("text search reads the diff: %+v", txt)
	}
	if cat, _ := st.ListChanges(ctx, ChangeQuery{Category: "states", From: 50}); len(cat) != 1 {
		t.Fatalf("category: %+v", cat)
	}
	if page, _ := st.ListChanges(ctx, ChangeQuery{Before: all[0].ID, Limit: 1}); len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("paging back: %+v", page)
	}

	if err := st.PruneChanges(ctx, 250); err != nil {
		t.Fatal(err)
	}
	left, _ := st.ListChanges(ctx, ChangeQuery{})
	if len(left) != 1 || left[0].At != 300 {
		t.Fatalf("after prune: %+v", left)
	}
	if host, _ := st.ListChanges(ctx, ChangeQuery{HostIDs: []string{"22"}}); len(host) != 0 {
		t.Fatalf("pruned entry still listed for its host: %+v", host)
	}
}

// A change keeps the sensors it was done to, for the chart markers: none is the whole host, and an
// entry written before the log kept them reads as not known.
func TestChangeItems(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.AddChanges(ctx, []Change{
		{At: 100, Actor: "Alice Rossi", Category: "states", Action: "Paused a sensor", Object: "web1 · CPU", HostIDs: []string{"7"}, ItemIDs: []string{"501", "", "501", "502"}},
		{At: 200, Actor: "Alice Rossi", Category: "hosts", Action: "Changed host settings", Object: "web1", HostIDs: []string{"7"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO changes (at, action, object) VALUES (50, 'Paused a sensor', 'web1 · Disk /')`); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListChanges(ctx, ChangeQuery{})
	if err != nil || len(got) != 3 {
		t.Fatalf("list: %+v %v", got, err)
	}
	byAt := map[int64]Change{}
	for _, c := range got {
		byAt[c.At] = c
	}
	if c := byAt[100]; !c.ItemsKnown || len(c.ItemIDs) != 2 || c.ItemIDs[0] != "501" || c.ItemIDs[1] != "502" {
		t.Fatalf("sensor entry (deduped, blanks dropped): %+v", c)
	}
	if c := byAt[200]; !c.ItemsKnown || len(c.ItemIDs) != 0 {
		t.Fatalf("host-wide entry: %+v", c)
	}
	if c := byAt[50]; c.ItemsKnown || len(c.ItemIDs) != 0 {
		t.Fatalf("an entry from before: %+v", c)
	}
}

// A push sensor's mail run counts at the mail's time; a mail that arrived late (older than the run on
// record) is counted but doesn't replace it. Deleting the mailbox takes the rule off.
func TestPushMailRuns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mb, err := st.CreateMailbox(ctx, Mailbox{Name: "Backups", Kind: "imap", Host: "imap.example.com", Port: 993, Security: "tls", Username: "u", Secret: "pw", Folder: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := st.GetMailbox(ctx, mb); err != nil || m.Secret != "pw" {
		t.Fatalf("mailbox: %+v %v", m, err)
	}
	id, err := st.CreatePushSensor(ctx, PushSensor{HostID: "7", Name: "Nightly", LateSecs: 3600, MissedSecs: 7200}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushMailRule(ctx, id, PushMailRule{Mailbox: mb, From: "veeam@", OK: "success"}); err != nil {
		t.Fatal(err)
	}
	t1 := time.Unix(2_000_000, 0)
	_ = st.RecordPushByID(ctx, id, true, "[Success] job", t1, "[Success] job")
	_ = st.RecordPushByID(ctx, id, false, "[Failed] job", t1.Add(-time.Hour), "[Failed] job")
	p, _ := st.GetPushSensor(ctx, id)
	if p.LastAt != t1.Unix() || !p.LastOK || p.LastMsg != "[Success] job" || p.Runs != 2 || p.MailLastSubject != "[Success] job" || p.Mail.From != "veeam@" {
		t.Fatalf("after a late mail: %+v", p)
	}
	if err := st.DeleteMailbox(ctx, mb); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetPushSensor(ctx, id); p.Mail.Mailbox != 0 {
		t.Fatalf("the rule stays after its mailbox went: %+v", p.Mail)
	}
}
