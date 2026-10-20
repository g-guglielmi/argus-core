// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"
	"time"

	"argus/internal/store"
)

func TestNoticeTargets(t *testing.T) {
	all := notifyDest{key: "g:1", kind: "g", id: 1, notices: true}
	site1 := notifyDest{key: "g:2", kind: "g", id: 2, notices: true, sites: []string{"site1"}}
	off := notifyDest{key: "g:3", kind: "g", id: 3, alerts: true} // alerts only
	mine := notifyDest{key: "u:7", kind: "u", id: 7, notices: true, userID: 42}
	dests := []notifyDest{all, site1, off, mine}

	keys := func(ds []notifyDest) map[string]bool {
		out := map[string]bool{}
		for _, d := range ds {
			out[d.key] = true
		}
		return out
	}
	// News about the whole install: every notice channel, whatever its sites.
	if k := keys(noticeTargets(notice{key: "argus-release:0.6.0"}, dests)); len(k) != 3 || k["g:3"] {
		t.Fatalf("install-wide: %v", k)
	}
	// A probe's news: channels serving its site (or all sites).
	if k := keys(noticeTargets(notice{key: "probe-behind:proxy-site2", site: "site2"}, dests)); len(k) != 2 || k["g:2"] {
		t.Fatalf("site2: %v", k)
	}

	// A failing shared channel isn't told about itself; a failing personal channel only reaches its
	// owner's other personal channels.
	chans := []store.NotifyChannel{{ID: 1, Name: "Team", LastErrorAt: 200, LastSentAt: 100, LastError: "HTTP 404"}, {ID: 2, LastErrorAt: 100, LastSentAt: 200}}
	users := []store.UserNotifyChannel{{ID: 8, UserID: 42, Type: "telegram", LastErrorAt: 300}}
	ns := channelFailingNotices(chans, users)
	if len(ns) != 2 {
		t.Fatalf("failing notices: %+v", ns)
	}
	if k := keys(noticeTargets(ns[0], dests)); k["g:1"] || !k["g:2"] || !k["u:7"] {
		t.Fatalf("shared channel failing: %v", k)
	}
	if ns[1].title != "Your Telegram channel is failing" {
		t.Fatalf("personal title: %q", ns[1].title)
	}
	if k := keys(noticeTargets(ns[1], dests)); len(k) != 1 || !k["u:7"] {
		t.Fatalf("personal channel failing: %v", k)
	}
}

func TestVersionMatchesTag(t *testing.T) {
	for _, c := range []struct {
		v, tag string
		want   bool
	}{{"7.0.31-r3", "7.0.31-r3", true}, {"0.2.5", "v0.2.5", true}, {"7.0.31-r2", "7.0.31-r3", false}, {"", "7.0.31-r3", false},
		{"7.0.31-r4", "latest", false}, {"0.2.6", "testing", false}, {"0.2.6", "", false}} {
		if got := versionMatchesTag(c.v, c.tag); got != c.want {
			t.Errorf("versionMatchesTag(%q, %q) = %v", c.v, c.tag, got)
		}
	}
}

func TestFleetUpdateNoticesArePooled(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	ev := func(what, version, site, prev string) notice {
		return notice{key: what + "-updated:proxy-" + site + ":" + version, pool: what + "|" + version, member: site + " (was " + prev + ")"}
	}
	// Three probes finish within the hold: nothing goes out until it is over, then one message.
	pool, due := poolNotices(noticePool{}, []notice{ev("probe", "7.0.31-r32", "mybz", "7.0.31-r31"), ev("probe", "7.0.31-r32", "myng", "7.0.31-r31")}, t0)
	if len(due) != 0 || len(pool.Groups) != 1 || len(pool.Groups[0].Members) != 2 {
		t.Fatalf("first tick: due=%d pool=%+v", len(due), pool)
	}
	pool, due = poolNotices(pool, []notice{ev("probe", "7.0.31-r32", "myrn", "7.0.31-r31")}, t0.Add(2*time.Minute))
	if len(due) != 0 || len(pool.Groups[0].Members) != 3 {
		t.Fatalf("second tick: due=%d pool=%+v", len(due), pool)
	}
	pool, due = poolNotices(pool, nil, t0.Add(noticePoolHold+time.Second))
	if len(due) != 1 || len(pool.Groups) != 0 {
		t.Fatalf("after the hold: due=%d pool=%+v", len(due), pool)
	}
	if due[0].title != "3 probes updated to 7.0.31-r32" || due[0].detail != "mybz (was 7.0.31-r31), myng (was 7.0.31-r31), myrn (was 7.0.31-r31)." || due[0].view != "updates" {
		t.Errorf("pooled notice: %q / %q", due[0].title, due[0].detail)
	}
	if due[0].key != "probe-updated-pool:7.0.31-r32:1700000000" {
		t.Errorf("pooled key: %q", due[0].key)
	}

	// The updaters, and a version on its own, pool apart; one member keeps the single wording.
	pool, _ = poolNotices(noticePool{}, []notice{ev("updater", "v0.2.16", "mybz", "v0.2.15"), ev("updater", "v0.2.16", "myng", "v0.2.15"), ev("probe", "7.0.32-r1", "ovh", "7.0.31-r32")}, t0)
	if len(pool.Groups) != 2 {
		t.Fatalf("groups: %+v", pool.Groups)
	}
	_, due = poolNotices(pool, nil, t0.Add(noticePoolHold))
	if len(due) != 2 {
		t.Fatalf("due: %d", len(due))
	}
	titles := map[string]notice{}
	for _, n := range due {
		titles[n.title] = n
	}
	if n, ok := titles["The updaters of 2 probes updated to v0.2.16"]; !ok || n.detail != "mybz (was v0.2.15), myng (was v0.2.15)." {
		t.Errorf("updater pool: %+v", due)
	}
	if n, ok := titles["Probe ovh updated to 7.0.32-r1"]; !ok || n.detail != "It was on 7.0.31-r32." || n.site != "ovh" || n.host != "Probe ovh" {
		t.Errorf("single: %+v", due)
	}

	// A straggler after the hold starts a pool of its own, told after its own hold.
	pool, due = poolNotices(noticePool{}, []notice{ev("probe", "7.0.31-r32", "late", "7.0.31-r31")}, t0.Add(10*time.Minute))
	if len(due) != 0 || len(pool.Groups) != 1 || pool.Groups[0].First != t0.Add(10*time.Minute).Unix() {
		t.Errorf("straggler: due=%d pool=%+v", len(due), pool)
	}
}
