// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// Two complementary check-ins model the socket-holding-sidecar deployment: the proxy container
// reports its real version and its network-scan capability but omits self-update capability (no
// socket), while the sidecar advertises self-update capability but reports no version and no scan
// capability. Neither may clobber the other's fields.
func TestRecordProbeCheckinStickyFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	yes, no := true, false

	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-a"); err != nil {
		t.Fatal(err)
	}

	// The proxy container reports its real version + scan capability but omits selfupdate (nil = leave it).
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.30-r2", nil, &yes); err != nil {
		t.Fatal(err)
	}
	// The sidecar checks in with no version, selfupdate=true, and no scans field.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "", &yes, nil); err != nil {
		t.Fatal(err)
	}

	ag, err := st.ProbeAgentByName(ctx, "proxy-a")
	if err != nil {
		t.Fatal(err)
	}
	if ag.Version != "7.0.30-r2" {
		t.Errorf("version = %q, want it preserved as 7.0.30-r2 (empty check-in must not clobber it)", ag.Version)
	}
	if !ag.SelfUpdate {
		t.Error("selfupdate should be true after the sidecar advertised capability")
	}
	if !ag.Scans {
		t.Error("scans must stay true when the sidecar's check-in omits it")
	}
	if ag.LastCheckin == 0 {
		t.Error("last_checkin should have been updated by the empty-version check-in")
	}

	// The proxy reports version again, omitting both flags: they must stick.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.31-r1", nil, nil); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.Version != "7.0.31-r1" {
		t.Errorf("version = %q, want 7.0.31-r1", ag.Version)
	}
	if !ag.SelfUpdate {
		t.Error("selfupdate must stay true when a later check-in omits it")
	}
	if !ag.Scans {
		t.Error("scans must stay true when a later check-in omits it")
	}

	// Explicit false reports do turn the flags off.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "", &no, &no); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.SelfUpdate {
		t.Error("selfupdate should be false after an explicit false report")
	}
	if ag.Scans {
		t.Error("scans should be false after an explicit false report")
	}
}
