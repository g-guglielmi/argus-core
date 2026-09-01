package store

import (
	"context"
	"testing"
)

// The compose poll sidecar checks in only to advertise self-update capability + read the fleet
// target; it reports no version. That check-in must not erase the authoritative version the proxy
// container itself reports.
func TestRecordProbeCheckinEmptyVersionKeepsPrior(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-a"); err != nil {
		t.Fatal(err)
	}

	// The proxy container reports its real version.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.30-r2", false); err != nil {
		t.Fatal(err)
	}
	// The sidecar checks in with no version but selfupdate=true.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "", true); err != nil {
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
	if ag.LastCheckin == 0 {
		t.Error("last_checkin should have been updated by the empty-version check-in")
	}

	// A later real version report still wins.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.31-r1", true); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.Version != "7.0.31-r1" {
		t.Errorf("version = %q, want 7.0.31-r1", ag.Version)
	}
}
