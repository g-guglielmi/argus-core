// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"strings"
	"testing"
)

// A scenario is checked before Zabbix sees it: names a key can carry, full http(s) URLs, status codes,
// form fields one name=value a line, secrets named like macros.
func TestWebScenarioValidate(t *testing.T) {
	ok := wsReq{Name: "Portal login", Every: "5m", Steps: []wsStepReq{
		{Name: "Open the login page", URL: "https://portal.example.com/login", Required: "Sign in", Status: "200"},
		{Name: "Sign in", URL: "https://portal.example.com/login", Posts: "user=monitor\npassword={$WEB.PASSWORD}\n", Required: "Welcome", Status: "200,302"},
		{URL: "https://portal.example.com/dashboard"},
	}, Secrets: map[string]string{"{$WEB.PASSWORD}": "s3cret"}}
	tt, msg := ok.validate("7")
	if msg != "" || len(tt.Steps) != 3 || tt.Steps[2].Name != "Step 3" || len(tt.Steps[1].Posts) != 2 || tt.Steps[1].Posts[1].Value != "{$WEB.PASSWORD}" {
		t.Fatalf("ok: %q %+v", msg, tt)
	}
	for name, bad := range map[string]wsReq{
		"comma in name":  {Name: "a,b", Every: "5m", Steps: ok.Steps},
		"no steps":       {Name: "x", Every: "5m"},
		"every":          {Name: "x", Every: "7m", Steps: ok.Steps},
		"url":            {Name: "x", Every: "5m", Steps: []wsStepReq{{URL: "ftp://x.example.com"}}},
		"status":         {Name: "x", Every: "5m", Steps: []wsStepReq{{URL: "https://x.example.com", Status: "OK"}}},
		"post":           {Name: "x", Every: "5m", Steps: []wsStepReq{{URL: "https://x.example.com", Posts: "novalue"}}},
		"same step name": {Name: "x", Every: "5m", Steps: []wsStepReq{{Name: "A", URL: "https://x.example.com"}, {Name: "a", URL: "https://x.example.com"}}},
		"secret name":    {Name: "x", Every: "5m", Steps: ok.Steps, Secrets: map[string]string{"password": "x"}},
	} {
		if _, msg := bad.validate("7"); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Each scenario gets a failure trigger and, per step, a slow and a very slow one on the template's
// thresholds, all tagged with the scenario and saying what came back.
func TestWebScenarioTriggers(t *testing.T) {
	tt, _ := wsReq{Name: "Portal login", Every: "5m", Steps: []wsStepReq{{Name: "Sign in", URL: "https://portal.example.com/login"}}}.validate("7")
	tt.HTTPTestID = "55"
	ts := wsTriggers("web1", tt)
	if len(ts) != 3 {
		t.Fatalf("triggers: %+v", ts)
	}
	if ts[0].Expression != "last(/web1/web.test.fail[Portal login])<>0" || ts[0].Priority != 4 || ts[0].Tags[wsTagKey] != "55" || ts[0].Tags["argus.recovery"] == "" {
		t.Errorf("failure: %+v", ts[0])
	}
	if !strings.Contains(ts[1].Expression, "web.test.time[Portal login,Sign in,resp])>{$WEB.TIME.WARN}") || ts[1].Priority != 2 {
		t.Errorf("slow: %+v", ts[1])
	}
	if ts[2].Expression != "last(/web1/web.test.time[Portal login,Sign in,resp])>{$WEB.TIME.HIGH}" || ts[2].Priority != 4 {
		t.Errorf("very slow: %+v", ts[2])
	}
	// The result reads as words; the reason shows while a step failed.
	if r, _ := runningReading("web.test.fail[Portal login]", "0"); r != "OK" {
		t.Errorf("ok reads %q", r)
	}
	if r, _ := runningReading("web.test.fail[Portal login]", "2"); r != "Step 2 failed" {
		t.Errorf("failed reads %q", r)
	}
	if !reasonShows("web.test.fail[Portal login]", "2") || reasonShows("web.test.fail[Portal login]", "0") || reasonKeyFor("web.test.fail[Portal login]") != "web.test.error[Portal login]" {
		t.Error("the reason follows a failed step")
	}
}
