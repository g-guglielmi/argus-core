// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

func TestViewToSNMPCarriesMaskedPassphrases(t *testing.T) {
	cur := &zabbix.SNMPDetails{AuthPassphrase: "old-auth", PrivPassphrase: "old-priv"}

	// Blank passphrases from the browser (masked, unchanged) must keep the stored ones.
	got := viewToSNMP(&snmpView{Version: 3, AuthPassphrase: "", PrivPassphrase: ""}, cur)
	if got.AuthPassphrase != "old-auth" || got.PrivPassphrase != "old-priv" {
		t.Errorf("blank passphrases should carry forward, got auth=%q priv=%q", got.AuthPassphrase, got.PrivPassphrase)
	}

	// A re-entered passphrase overrides the stored one.
	got = viewToSNMP(&snmpView{Version: 3, AuthPassphrase: "new-auth", PrivPassphrase: ""}, cur)
	if got.AuthPassphrase != "new-auth" || got.PrivPassphrase != "old-priv" {
		t.Errorf("re-entered auth should win; got auth=%q priv=%q", got.AuthPassphrase, got.PrivPassphrase)
	}

	// No current interface (a brand-new one) leaves blanks blank.
	got = viewToSNMP(&snmpView{Version: 3}, nil)
	if got.AuthPassphrase != "" || got.PrivPassphrase != "" {
		t.Errorf("new interface should have empty passphrases, got auth=%q priv=%q", got.AuthPassphrase, got.PrivPassphrase)
	}
}

func TestDefaultPort(t *testing.T) {
	for _, c := range []struct {
		typ  int
		want string
	}{{1, "10050"}, {2, "161"}, {3, "623"}, {4, "12345"}} {
		if got := defaultPort(c.typ); got != c.want {
			t.Errorf("defaultPort(%d) = %q, want %q", c.typ, got, c.want)
		}
	}
}

// An option under a third party's terms is set only with them accepted; one already set stays.
func TestTermsRefusal(t *testing.T) {
	ms := provision.MacroSpec{Macro: "{$SPEEDTEST.ENGINE}", OptionLabels: map[string]string{"ookla": "Ookla Speedtest"},
		Terms: &provision.Terms{Value: "ookla", Title: "Ookla's terms"}}
	none := map[string]zabbix.HostMacro{}
	if err := termsRefusal("Speedtest", ms, "ookla", addOnDesired{}, none); err == nil || err.Error() != "Speedtest: accept Ookla's terms to choose Ookla Speedtest" {
		t.Fatalf("not accepted: %v", err)
	}
	if err := termsRefusal("Speedtest", ms, "ookla", addOnDesired{Accepted: []string{"{$SPEEDTEST.ENGINE}"}}, none); err != nil {
		t.Fatalf("accepted: %v", err)
	}
	set := map[string]zabbix.HostMacro{"{$SPEEDTEST.ENGINE}": {Macro: "{$SPEEDTEST.ENGINE}", Value: "ookla"}}
	if err := termsRefusal("Speedtest", ms, "ookla", addOnDesired{}, set); err != nil {
		t.Fatalf("already set: %v", err)
	}
	if err := termsRefusal("Speedtest", ms, "cloudflare", addOnDesired{}, none); err != nil {
		t.Fatalf("an option without terms: %v", err)
	}
}
