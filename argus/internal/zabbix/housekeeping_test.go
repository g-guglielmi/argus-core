// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"fmt"
	"testing"
)

func TestParsePeriod(t *testing.T) {
	cases := map[string]int64{"30d": 30 * 86400, "2w": 14 * 86400, "12h": 43200, "90m": 5400, "3600s": 3600, "86400": 86400}
	for in, want := range cases {
		if got, err := ParsePeriod(in); err != nil || got != want {
			t.Errorf("ParsePeriod(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "x1d", "-3d", "1y"} {
		if _, err := ParsePeriod(bad); err == nil {
			t.Errorf("ParsePeriod(%q) should fail", bad)
		}
	}
}

func TestIsPermissionError(t *testing.T) {
	perm := &rpcError{Code: -32500, Message: "Application error.", Data: `No permissions to call "housekeeping.get".`}
	if !IsPermissionError(perm) || !IsPermissionError(fmt.Errorf("wrapped: %w", perm)) {
		t.Fatal("permission error not detected")
	}
	if IsPermissionError(&rpcError{Code: -32602, Message: "Invalid params.", Data: "bad value"}) || IsPermissionError(fmt.Errorf("plain")) {
		t.Fatal("false positive")
	}
}
