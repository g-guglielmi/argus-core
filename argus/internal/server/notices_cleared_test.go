// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
	"time"

	"argus/internal/store"
)

// A told condition's all-clear says what came back and how long it held, in the same [INFO] shape,
// linking where the condition did; a condition told without a since just says it cleared.
func TestClearedEventWording(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 10, 16, 14, 30, 0, 0, loc)
	f := store.SentNotice{Key: "probe-report:p1", Kind: store.NoticeFact, Since: time.Date(2026, 10, 16, 11, 3, 0, 0, loc).Unix(),
		Cleared: "Probe site1 is reporting to Argus again", Host: "Probe site1", Site: "site1", View: "probes"}
	ev := clearedEvent(f, now, "https://argus.example.com/")
	if ev.Kind != "info" || ev.State != "ok" || ev.Name != f.Cleared || ev.Host != "Probe site1" || ev.Site != "site1" {
		t.Fatalf("event shape: %+v", ev)
	}
	if ev.Detail != "It held since Oct 16, 11:03, for 3h 27m." {
		t.Errorf("detail: %q", ev.Detail)
	}
	if ev.OpenURL != "https://argus.example.com/?view=probes" {
		t.Errorf("link: %q", ev.OpenURL)
	}
	if ev.SinceSecs != 3*3600+27*60 {
		t.Errorf("since: %d", ev.SinceSecs)
	}
	ev = clearedEvent(store.SentNotice{Cleared: "Core backup succeeded again"}, now, "")
	if ev.Detail != "Cleared." || ev.OpenURL != "" {
		t.Errorf("no since: %+v", ev)
	}
}

// Every condition with an all-clear names what came back, not what was wrong: no "failing",
// "pending" or "isn't" in an all-clear's title.
func TestClearedTitlesSayWhatCameBack(t *testing.T) {
	for _, title := range []string{
		"Probe site1 is reporting to Argus again",
		"Notification channel delivering again: Ops",
		"Core backup succeeded again",
		"Core backups are back on schedule",
		"Core backup export works again",
		"Security updates applied on the core VM",
		"The core VM rebooted",
	} {
		for _, bad := range []string{"failing", "pending", "isn't", "failed", "needs"} {
			if strings.Contains(title, bad) {
				t.Errorf("all-clear %q reads like the problem (%q)", title, bad)
			}
		}
	}
}
