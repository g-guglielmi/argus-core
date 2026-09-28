// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/store"
)

func planKeys(plan []plannedDelivery) map[string]bool {
	out := map[string]bool{}
	for _, p := range plan {
		k := p.dest.key
		if p.reminder {
			k += "+r"
		}
		out[k] = true
	}
	return out
}

func TestPlanDeliveries(t *testing.T) {
	// Incident started at 1000 and went live (passed the alert delay) at 1060.
	const start, fired = int64(1000), int64(1060)
	team := notifyDest{key: "g:1", kind: "g", id: 1, minSev: 2, repeat: 1800, created: 0}   // at once, remind every 30m
	manager := notifyDest{key: "g:2", kind: "g", id: 2, minSev: 2, delay: 1800, created: 0} // after 30m
	oncall := notifyDest{key: "u:7", kind: "u", id: 7, minSev: 4, created: 0}               // High and up only
	other := notifyDest{key: "g:3", kind: "g", id: 3, minSev: 2, sites: []string{"site2"}}  // another site
	late := notifyDest{key: "g:4", kind: "g", id: 4, minSev: 2, created: 5000}              // added after the problem went live
	dests := []notifyDest{team, manager, oncall, other, late}
	groups := []string{"site1/Servers"}

	// Right after going live: only the immediate channel that serves the site and severity.
	if got := planKeys(planDeliveries(dests, nil, groups, 2, start, fired, fired)); len(got) != 1 || !got["g:1"] {
		t.Fatalf("first alert: %v", got)
	}

	// 30 minutes into the incident the manager's delay is up; the team has had it since 1060 and its
	// 30-minute reminder isn't due yet (1060 + 1800 = 2860).
	got := map[string]store.NotifyDelivery{"g:1": {Kind: "g", ChannelID: 1, Severity: 2, FirstSent: fired, LastSent: fired}}
	if p := planKeys(planDeliveries(dests, got, groups, 2, start, fired, start+1800)); len(p) != 1 || !p["g:2"] {
		t.Fatalf("escalation at 30m: %v", p)
	}

	// At 2860 the team's reminder is due; the manager (no reminders) stays quiet once told.
	got["g:2"] = store.NotifyDelivery{Kind: "g", ChannelID: 2, Severity: 2, FirstSent: 2800, LastSent: 2800}
	if p := planKeys(planDeliveries(dests, got, groups, 2, start, fired, 2860)); len(p) != 1 || !p["g:1+r"] {
		t.Fatalf("reminder: %v", p)
	}

	// Escalating to High on the same sensor: the channels that had the warning get the High alert
	// straight away (even the delayed manager, already involved), and the High-only channel joins.
	p := planKeys(planDeliveries(dests, got, groups, 4, start, 3000, 3000))
	if len(p) != 3 || !p["g:1"] || !p["g:2"] || !p["u:7"] {
		t.Fatalf("severity change: %v", p)
	}

	// "Remind for" High and up: the warning is alerted but never reminded; the error is.
	strict := notifyDest{key: "g:6", kind: "g", id: 6, minSev: 2, repeat: 900, remSev: 4}
	sent := map[string]store.NotifyDelivery{"g:6": {Kind: "g", ChannelID: 6, Severity: 2, LastSent: fired}}
	if p := planKeys(planDeliveries([]notifyDest{strict}, sent, groups, 2, start, fired, fired+3600)); len(p) != 0 {
		t.Fatalf("warning reminded below the remind-for floor: %v", p)
	}
	sent["g:6"] = store.NotifyDelivery{Kind: "g", ChannelID: 6, Severity: 4, LastSent: fired}
	if p := planKeys(planDeliveries([]notifyDest{strict}, sent, groups, 4, start, fired, fired+900)); !p["g:6+r"] {
		t.Fatalf("error not reminded: %v", p)
	}

	// An alert that went live before a channel existed isn't replayed at it.
	if p := planKeys(planDeliveries([]notifyDest{late}, nil, groups, 2, start, fired, 6000)); len(p) != 0 {
		t.Fatalf("new channel replayed an old alert: %v", p)
	}

	// The delay counts from when the incident began, but never sends before the alert went live.
	quick := notifyDest{key: "g:5", kind: "g", id: 5, minSev: 2, delay: 30}
	if p := planKeys(planDeliveries([]notifyDest{quick}, nil, groups, 2, start, fired, 1040)); len(p) != 0 {
		t.Fatalf("sent before going live: %v", p)
	}
}
