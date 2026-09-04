package store

import (
	"context"
	"testing"
)

// A probe reports its OS patch status; a re-report overwrites it, a negative count clamps to -1, and
// an unknown probe is rejected (nothing to attach the status to).
func TestSetProbeOSStatus(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.SetProbeOSStatus(ctx, "proxy-missing", 3, true); err != ErrNotFound {
		t.Fatalf("SetProbeOSStatus on an unknown probe = %v, want ErrNotFound", err)
	}

	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-a"); err != nil {
		t.Fatal(err)
	}
	// Fresh agent: sec_updates defaults to -1 (unknown), never reported.
	ag, err := st.ProbeAgentByName(ctx, "proxy-a")
	if err != nil {
		t.Fatal(err)
	}
	if ag.SecUpdates != -1 || ag.RebootRequired || ag.OSReportedAt != 0 {
		t.Fatalf("fresh agent OS status = {%d,%v,%d}, want {-1,false,0}", ag.SecUpdates, ag.RebootRequired, ag.OSReportedAt)
	}

	if err := st.SetProbeOSStatus(ctx, "proxy-a", 5, true); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.SecUpdates != 5 || !ag.RebootRequired || ag.OSReportedAt == 0 {
		t.Fatalf("after report = {%d,%v,%d}, want {5,true,>0}", ag.SecUpdates, ag.RebootRequired, ag.OSReportedAt)
	}

	// A later report overwrites; a negative count clamps to -1 (unknown), reboot clears.
	if err := st.SetProbeOSStatus(ctx, "proxy-a", -7, false); err != nil {
		t.Fatal(err)
	}
	ag, _ = st.ProbeAgentByName(ctx, "proxy-a")
	if ag.SecUpdates != -1 || ag.RebootRequired {
		t.Fatalf("after negative report = {%d,%v}, want {-1,false}", ag.SecUpdates, ag.RebootRequired)
	}
}
