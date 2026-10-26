// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/store"
)

// A sensor's kind is its key with the instance as "*": every mount's used space is one kind, its free
// space another; a key without parameters is its own kind.
func TestKeyKind(t *testing.T) {
	for in, want := range map[string]string{
		"vfs.fs.size[/data,pused]": "vfs.fs.size[*,pused]", "vfs.fs.size[/,free]": "vfs.fs.size[*,free]", "net.if.in[eth0]": "net.if.in[*]",
		"icmpping": "icmpping", "saas.up[52a3d816b2]": "saas.up[*]", "unifi.device.state": "unifi.device.state",
	} {
		if got := keyKind(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// A sensor's own runbook wins over its kind's; a kind's reaches the sensors of that kind on hosts of
// that class only.
func TestRunbookIndex(t *testing.T) {
	ix := runbookIndex{
		items:   map[string]store.Runbook{"501": {Scope: "item", Text: "own"}},
		kinds:   map[string]store.Runbook{"unifi-ap|icmpping": {Scope: "kind", Text: "every AP ping"}},
		classes: map[string]string{"7": "unifi-ap", "8": "unifi-ap", "9": "linux-snmp"},
	}
	cases := []struct{ host, item, key, want string }{
		{"7", "501", "icmpping", "own"},
		{"8", "601", "icmpping", "every AP ping"},
		{"9", "701", "icmpping", ""},
		{"8", "602", "icmppingloss", ""},
	}
	for _, c := range cases {
		got := ""
		if v := ix.view(c.host, c.item, c.key); v != nil {
			got = v.Text
		}
		if got != c.want {
			t.Errorf("host %s item %s: got %q, want %q", c.host, c.item, got, c.want)
		}
	}
}
