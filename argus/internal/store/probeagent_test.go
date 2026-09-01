package store

import (
	"context"
	"testing"
)

// Two complementary check-ins model the socket-holding-sidecar deployment: the proxy container
// reports its real version but omits self-update capability (no socket), while the sidecar advertises
// capability but reports no version. Neither may clobber the other's field.
func TestRecordProbeCheckinStickyFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	yes, no := true, false

	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-a"); err != nil {
		t.Fatal(err)
	}

	// The proxy container reports its real version but omits selfupdate (nil = leave it).
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.30-r2", nil); err != nil {
		t.Fatal(err)
	}
	// The sidecar checks in with no version but selfupdate=true.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "", &yes); err != nil {
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

	// The proxy reports version again, still omitting selfupdate: the flag must stick at true.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "7.0.31-r1", nil); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.Version != "7.0.31-r1" {
		t.Errorf("version = %q, want 7.0.31-r1", ag.Version)
	}
	if !ag.SelfUpdate {
		t.Error("selfupdate must stay true when a later check-in omits it")
	}

	// An explicit selfupdate=false does turn it off.
	if err := st.RecordProbeCheckin(ctx, "proxy-a", "", &no); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.SelfUpdate {
		t.Error("selfupdate should be false after an explicit false report")
	}
}
