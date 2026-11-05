// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Removed hosts take their own rows with them; other hosts' rows and the history stay, and a
// discovered device that had become one is offered again.
func TestDeleteHostRecords(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, h := range []string{"101", "102", "200"} {
		if err := st.SetDeviceClass(ctx, h, "base", "auto"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSuppression(ctx, "hide", "host", h, 1, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO discovery_jobs(id, proxy_name, cidr, created_at) VALUES(1, 'proxy-kit1', '10.0.0.0/24', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO discovery_results(job_id, ip, state, host_id) VALUES(1, '10.0.0.5', 'added', '101')`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteHostRecords(ctx, []string{"101", "102"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_class`).Scan(&n)
	if n != 1 {
		t.Fatalf("device_class rows = %d, want only host 200's", n)
	}
	hidden, _ := st.ActiveSuppressionMap(ctx, "hide", "host")
	if _, ok := hidden["200"]; !ok || len(hidden) != 1 {
		t.Fatalf("hidden = %v, want only 200", hidden)
	}
	var state, host string
	_ = st.db.QueryRowContext(ctx, `SELECT state, host_id FROM discovery_results WHERE ip='10.0.0.5'`).Scan(&state, &host)
	if state != "new" || host != "" {
		t.Fatalf("discovered device = %s %q, want offered again", state, host)
	}
	if err := st.DeleteHostRecords(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
