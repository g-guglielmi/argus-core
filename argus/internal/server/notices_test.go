// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

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
