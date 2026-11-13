// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"encoding/json"
	"testing"
)

// One Microsoft 365 post for the whole suite, listing the services it hits: Outlook reads Exchange
// Online's line, Teams Teams' line, Microsoft 365 the post's title; a service the list leaves out has
// nothing open. A post with no such list counts for every service.
func TestM365PostPerService(t *testing.T) {
	msg := `<div>User impact: Users may be unable to connect to multiple Microsoft 365 services. <div><br></div>` +
		`<div>More info: Features of the Microsoft 365 services below that interact with Exchange Online are impacted.</div><div>&nbsp;</div>` +
		`<div>- Exchange Online: Users may be unable to access their mailbox using Outlook on the web, and email delivery may be delayed. More text.</div>` +
		`<div>- Microsoft 365 admin center: Admins may be unable to load some admin center pages.</div>` +
		`<div>- Microsoft Teams: Most of the core functionality including joining calls &amp; meetings may be impacted.</div></div>`
	body, _ := json.Marshal(map[string]string{"Title": "Users may be unable to connect to multiple Microsoft 365 services", "Message": msg,
		"Status": "Service degradation", "AuthoredDateTime": "2026-11-13T07:29:09+00:00"})

	var o vendorWord
	if err := m365PostWord(&o, body, tenantServices["outlook"]); err != nil || o.Open != 1 ||
		o.Title != "Users may be unable to access their mailbox using Outlook on the web, and email delivery may be delayed. More text." {
		t.Fatalf("outlook: %+v %v", o, err)
	}
	var tm vendorWord
	if err := m365PostWord(&tm, body, tenantServices["teams"]); err != nil || tm.Title != "Most of the core functionality including joining calls & meetings may be impacted." {
		t.Fatalf("teams: %+v %v", tm, err)
	}
	// A long line keeps the whole sentences that fit: what works first, then what fails.
	long := "Most of the core functionality including joining calls, meetings, group chats, and channel messages are not impacted. " +
		"Scenarios that depend on Exchange Online, including Calendar related failures with scheduling, invites, viewing, and meeting edits. " +
		"Creation of Live or Virtual events fail. Failures with Copilot responses for outlook, calendar, or M365 files, read history, or adding to new chats."
	want := "Most of the core functionality including joining calls, meetings, group chats, and channel messages are not impacted. " +
		"Scenarios that depend on Exchange Online, including Calendar related failures with scheduling, invites, viewing, and meeting edits."
	if got := sentencesUpTo(long, vendorTitleMax); got != want {
		t.Fatalf("cut: %q", got)
	}
	if got := sentencesUpTo("short.", vendorTitleMax); got != "short." {
		t.Fatalf("short: %q", got)
	}
	if got := []rune(sentencesUpTo(string(make([]rune, 400)), 10)); len(got) > 10 {
		t.Fatalf("one long sentence: %d", len(got))
	}
	var m vendorWord
	if err := m365PostWord(&m, body, tenantServices["m365"]); err != nil || m.Title != "Users may be unable to connect to multiple Microsoft 365 services" {
		t.Fatalf("m365: %+v %v", m, err)
	}
	// Teams left out of the list: nothing open for it.
	only, _ := json.Marshal(map[string]string{"Title": "Mail delays", "Status": "Service degradation",
		"Message": `<div>- Exchange Online: Email delivery may be delayed.</div>`})
	var t2 vendorWord
	if err := m365PostWord(&t2, only, tenantServices["teams"]); err != nil || t2.Open != 0 {
		t.Fatalf("teams not listed: %+v %v", t2, err)
	}
	// No list: every service shares the post.
	plain, _ := json.Marshal(map[string]string{"Title": "Sign-in failures", "Status": "Service interruption", "Message": "<div>We are investigating.</div>"})
	var o2 vendorWord
	if err := m365PostWord(&o2, plain, tenantServices["outlook"]); err != nil || o2.Open != 1 || o2.Title != "Sign-in failures" || o2.Impact != "major" {
		t.Fatalf("no list: %+v %v", o2, err)
	}
}
