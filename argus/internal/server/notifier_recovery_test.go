// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// A RESOLVED message says what came back: the trigger's own sentence, the obvious one for Argus's own
// problems, and "Back to normal" for a trigger from outside Argus's templates.
func TestRecoveryTitle(t *testing.T) {
	p := zabbix.Problem{Name: "GitHub does not answer from this site", Tags: []zabbix.ProblemTag{
		{Tag: "scope", Value: "availability"},
		{Tag: "argus.recovery", Value: " GitHub answers again from this site "},
	}}
	if got := recoveryOf(p); got != "GitHub answers again from this site" {
		t.Errorf("recoveryOf = %q", got)
	}
	if got := recoveryOf(zabbix.Problem{Name: "x"}); got != "" {
		t.Errorf("recoveryOf without the tag = %q", got)
	}
	cases := []struct {
		stt  store.NotifyState
		want string
	}{
		{store.NotifyState{EventID: "123", Name: "GitHub does not answer from this site", Recovery: "GitHub answers again from this site"}, "GitHub answers again from this site"},
		{store.NotifyState{EventID: synthUnsupported + "77", Name: "GitHub response time · Response time stopped collecting"}, "GitHub response time · Response time is collecting again"},
		{store.NotifyState{EventID: synthUnsupported + "78", Name: "HTTP checks stopped collecting"}, "HTTP checks are collecting again"},
		{store.NotifyState{EventID: synthInterface + "5", Name: interfaceDownName("2")}, "SNMP is responding again"},
		{store.NotifyState{EventID: synthInterface + "6", Name: interfaceDownName("1")}, "Zabbix agent is reachable again"},
		{store.NotifyState{EventID: "124", Name: "Linux: High memory utilization"}, "Back to normal: Linux: High memory utilization"},
		// A Zabbix trigger merely named like Argus's own problem keeps the generic wording.
		{store.NotifyState{EventID: "125", Name: "Backup job stopped collecting"}, "Back to normal: Backup job stopped collecting"},
	}
	for _, c := range cases {
		if got := recoveryTitle(c.stt); got != c.want {
			t.Errorf("recoveryTitle(%q) = %q, want %q", c.stt.Name, got, c.want)
		}
	}
	for _, typ := range []string{"1", "2", "3", "4", "9"} {
		if interfaceUpName(interfaceDownName(typ)) == "" {
			t.Errorf("interface type %s has no recovery wording", typ)
		}
	}
}
