package server

import (
	"testing"

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
