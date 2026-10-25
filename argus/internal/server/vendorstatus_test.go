// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"
	"time"
)

// A SaaS sensor finds its vendor by the id argus_http.py gives its URL (the first 10 hex of its
// SHA-1), whatever the service is called.
func TestSaaSServiceOfKey(t *testing.T) {
	if svc, ok := saasServiceOfKey("saas.up[52a3d816b2]"); !ok || svc.ID != "github" || svc.Status == nil {
		t.Fatalf("GitHub's reachability: %+v %v", svc, ok)
	}
	if svc, ok := saasServiceOfKey("saas.time[52a3d816b2]"); !ok || svc.ID != "github" {
		t.Fatalf("GitHub's response time: %+v %v", svc, ok)
	}
	if _, ok := saasServiceOfKey("saas.error[52a3d816b2]"); ok {
		t.Fatal("the error item is not a sensor of the service")
	}
	if _, ok := saasServiceOfKey("saas.up[0000000000]"); ok {
		t.Fatal("a URL outside the catalog has no vendor")
	}
}

// Statuspage: every unresolved incident counts, the worst impact names the word; Slack's own API
// ranks an outage above an incident; a page with only its indicator doesn't count maintenance.
func TestVendorWords(t *testing.T) {
	var w vendorWord
	if err := statuspageWord(&w, []byte(`{"incidents":[
		{"name":"Slow webhooks","impact":"minor","created_at":"2026-10-25T13:00:00Z"},
		{"name":"Git operations failing","impact":"major","created_at":"2026-10-25T14:05:00.000Z","started_at":"2026-10-25T14:01:00.000Z"},
		{"name":"Docs typo","impact":"none","created_at":"2026-10-25T12:00:00Z"}]}`)); err != nil {
		t.Fatal(err)
	}
	if w.Open != 3 || w.Impact != "major" || w.Title != "Git operations failing" || w.Since != time.Date(2026, 10, 25, 14, 1, 0, 0, time.UTC).Unix() {
		t.Fatalf("statuspage: %+v", w)
	}
	w = vendorWord{}
	if err := statuspageWord(&w, []byte(`{"page":{},"incidents":[]}`)); err != nil || w.Open != 0 || w.Title != "" {
		t.Fatalf("statuspage, nothing open: %+v %v", w, err)
	}
	w = vendorWord{}
	if err := slackWord(&w, []byte(`{"status":"active","active_incidents":[
		{"title":"Some users can't upload files","type":"incident","date_created":"2026-10-25T09:00:00-07:00"},
		{"title":"Slack is down","type":"outage","date_created":"2026-10-25T10:00:00-07:00"}]}`)); err != nil {
		t.Fatal(err)
	}
	if w.Open != 2 || w.Impact != "major" || w.Title != "Slack is down" || w.Since != time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("slack: %+v", w)
	}
	for body, open := range map[string]int{
		`{"status":{"indicator":"none","description":"All Systems Operational"}}`:          0,
		`{"status":{"indicator":"maintenance","description":"Service Under Maintenance"}}`: 0,
		`{"status":{"indicator":"minor","description":"Partially Degraded Service"}}`:      1,
	} {
		w = vendorWord{}
		if err := indicatorWord(&w, []byte(body)); err != nil || w.Open != open {
			t.Fatalf("indicator %s: %+v %v", body, w, err)
		}
	}
}

// The alert's line: none open, or the worst one with its impact, since when and how many more.
func TestVendorLine(t *testing.T) {
	loc := time.UTC
	if got := vendorLine(&vendorWord{Service: "GitHub"}, loc); got != "GitHub's status page: no incident open" {
		t.Fatalf("none: %q", got)
	}
	since := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	got := vendorLine(&vendorWord{Service: "Zoom", Open: 2, Impact: "minor", Title: "Degraded meeting joins", Since: since.Unix()}, loc)
	want := `Zoom's status page: minor incident "Degraded meeting joins" since ` + since.In(loc).Format("15:04") + " (and 1 more)"
	if got != want {
		t.Fatalf("open:\n got %q\nwant %q", got, want)
	}
	if got := vendorLine(&vendorWord{Service: "Slack", Open: 1, Impact: "none", Title: "Scheduled notice"}, loc); got != `Slack's status page: incident "Scheduled notice"` {
		t.Fatalf("no impact: %q", got)
	}
	if vendorLine(nil, loc) != "" {
		t.Fatal("no word, no line")
	}
}
