// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package settings

import (
	"reflect"
	"testing"
)

func TestParseHostList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"*", nil, false},
		{"monitoring.example.com, 10.0.0.10", []string{"monitoring.example.com", "10.0.0.10"}, false},
		{"https://Monitoring.Example.com:8443/login ; 10.0.0.10:8081", []string{"monitoring.example.com", "10.0.0.10"}, false},
		{"[fd00::10]:8081 fd00::10", []string{"fd00::10"}, false},
		{"a.example.lan a.example.lan. A.EXAMPLE.LAN", []string{"a.example.lan"}, false},
		{"a.example.lan, *", nil, false},
		{"*.example.lan", nil, true},
		{"bad_host", nil, true},
		{"-lead.example.com", nil, true},
	}
	for _, c := range cases {
		got, err := ParseHostList(c.in)
		if (err != nil) != c.err || (!c.err && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("ParseHostList(%q) = %v, %v; want %v (err=%v)", c.in, got, err, c.want, c.err)
		}
	}
}
