package store

import (
	"context"
	"testing"
	"time"
)

func TestProxyCleanup(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)

	// enroll tokens: a used one for a proxy later deleted out-of-band, a pending one whose proxy
	// doesn't exist yet, and a used one for a live proxy.
	goneTok, err := st.CreateEnrollToken(ctx, "h-gone", "proxy-gone", "gone", 1, exp)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkEnrollTokenUsed(ctx, goneTok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEnrollToken(ctx, "h-pending", "proxy-future", "future", 1, exp); err != nil {
		t.Fatal(err)
	}
	liveTok, err := st.CreateEnrollToken(ctx, "h-live", "proxy-live", "live", 1, exp)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.MarkEnrollTokenUsed(ctx, liveTok)

	// check-in state + SNMP defaults
	_ = st.UpsertProbeCredential(ctx, "proxy-gone", "t1")
	_ = st.UpsertProbeCredential(ctx, "proxy-live", "t2")
	_ = st.SetSNMPDefault(ctx, "99", SNMPDefault{Version: 2, Community: "x"})
	_ = st.SetSNMPDefault(ctx, "1", SNMPDefault{Version: 2, Community: "y"})

	// Reconcile against a world where only proxy-live (id 1) exists.
	pruned, err := st.ReconcileProxies(ctx, map[string]bool{"proxy-live": true}, map[string]bool{"1": true})
	if err != nil {
		t.Fatal(err)
	}
	// orphans pruned: probe_agents(proxy-gone), enroll_tokens(used proxy-gone), snmp_defaults(99) = 3
	if pruned != 3 {
		t.Errorf("pruned = %d, want 3", pruned)
	}

	// the pending token (proxy-future) must survive - its proxy doesn't exist yet by design.
	toks, _ := st.ListEnrollTokens(ctx)
	pending := false
	for _, tk := range toks {
		if tk.ProxyName == "proxy-future" {
			pending = true
		}
		if tk.ProxyName == "proxy-gone" {
			t.Error("used token for a deleted proxy was not pruned")
		}
	}
	if !pending {
		t.Error("pending token for proxy-future was wrongly pruned")
	}

	// DeleteProxyRecords removes everything for one proxy.
	if err := st.DeleteProxyRecords(ctx, "1", "proxy-live"); err != nil {
		t.Fatal(err)
	}
	if ag, _ := st.ProbeAgents(ctx); ag["proxy-live"].Version != "" || len(ag) != 0 {
		t.Errorf("proxy-live agent not deleted: %+v", ag)
	}
	if _, ok, _ := st.SNMPDefaultFor(ctx, "1"); ok {
		t.Error("proxy-live SNMP default not deleted")
	}
}
