// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"testing"

	"argus/internal/zabbix"
)

func TestRetentionFrom(t *testing.T) {
	v := retentionFrom(zabbix.Housekeeping{
		HistoryGlobal: "1", History: "30d", TrendsGlobal: "0", Trends: "2w",
		CompressionStatus: "1", CompressOlder: "604800", CompressionAvailability: "1", DBExtension: "timescaledb",
	})
	if !v.Available || v.HistoryDays != 30 || !v.HistoryOverride || v.TrendDays != 14 || v.TrendOverride {
		t.Fatalf("periods: %+v", v)
	}
	if !v.CompressionAvailable || !v.Compression || v.CompressAfterDays != 7 {
		t.Fatalf("compression: %+v", v)
	}
	if d := periodDays("36h"); d != 2 { // rounds to whole days
		t.Fatalf("36h -> %d days, want 2", d)
	}
	if d := periodDays("junk"); d != 0 {
		t.Fatalf("junk -> %d, want 0", d)
	}
}

// Zabbix 7.0 omits compression_availability, so TimescaleDB (db_extension) or compression already
// being on must count as available - the case that hid the section on a real TimescaleDB core.
func TestCompressionAvailable(t *testing.T) {
	cases := []struct {
		name string
		hk   zabbix.Housekeeping
		want bool
	}{
		{"7.0 TimescaleDB, field absent", zabbix.Housekeeping{DBExtension: "timescaledb", CompressionStatus: "1"}, true},
		{"TimescaleDB, compression off", zabbix.Housekeeping{DBExtension: "timescaledb"}, true},
		{"compression on, extension unreported", zabbix.Housekeeping{CompressionStatus: "1"}, true},
		{"older API reports it", zabbix.Housekeeping{CompressionAvailability: "1"}, true},
		{"plain PostgreSQL", zabbix.Housekeeping{}, false},
	}
	for _, c := range cases {
		if got := compressionAvailable(c.hk); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRetentionValidate(t *testing.T) {
	cases := []struct {
		name  string
		u     retentionUpdate
		avail bool
		ok    bool
	}{
		{"defaults", retentionUpdate{30, 730, true, 7}, true, true},
		{"history below the 2d tab", retentionUpdate{1, 730, false, 0}, true, false},
		{"trends below the 7d tab", retentionUpdate{30, 6, false, 0}, true, false},
		{"past the 25y cap", retentionUpdate{30, 9126, false, 0}, true, false},
		{"compression too early", retentionUpdate{30, 730, true, 3}, true, false},
		{"compression ignored without TimescaleDB", retentionUpdate{30, 730, true, 0}, false, true},
		{"compression off needs no period", retentionUpdate{30, 730, false, 0}, false, true},
	}
	for _, c := range cases {
		if err := c.u.validate(c.avail); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}
