// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

// A probe's addresses: the minute report sets its own and the seen one each time, a public one only
// when it carries a fresh lookup; the VM host's report sets the LAN pair; a re-enrollment forgets the
// container's addresses (it may be another machine on another network) but not the host's.
func TestProbeAddresses(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-a"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeReportAddrs(ctx, "proxy-a", "172.17.0.2", "10.0.1.20", "203.0.113.10", 1700000000); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeReportAddrs(ctx, "proxy-a", "172.17.0.3", "10.0.1.21", "", 1700000500); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeHostAddr(ctx, "proxy-a", "10.0.1.21", "10.0.1.1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeHostAddr(ctx, "proxy-a", "", "10.0.9.1"); err != nil { // an older reporter: ignored
		t.Fatal(err)
	}
	if err := st.SetProbeNet(ctx, "proxy-a", "macvlan"); err != nil {
		t.Fatal(err)
	}
	all, err := st.ProbeAddresses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := all["proxy-a"]
	if a.Own != "172.17.0.3" || a.Seen != "10.0.1.21" || a.At == 0 {
		t.Fatalf("report addresses = %q %q %d, want the last report's", a.Own, a.Seen, a.At)
	}
	if a.Public != "203.0.113.10" || a.PublicAt != 1700000000 {
		t.Fatalf("public = %q at %d, want the last lookup kept", a.Public, a.PublicAt)
	}
	if a.Net != "macvlan" {
		t.Fatalf("net = %q, want the sidecar's", a.Net)
	}
	if a.Host != "10.0.1.21" || a.HostGW != "10.0.1.1" || a.HostAt == 0 {
		t.Fatalf("host = %q via %q at %d, want the host's report", a.Host, a.HostGW, a.HostAt)
	}

	if err := st.UpsertProbeCredential(ctx, "proxy-a", "hash-b"); err != nil {
		t.Fatal(err)
	}
	all, _ = st.ProbeAddresses(ctx)
	if a := all["proxy-a"]; a.Own != "" || a.Seen != "" || a.Public != "" || a.Host != "10.0.1.21" {
		t.Fatalf("after re-enrollment = %+v, want the container's addresses gone, the host's kept", a)
	}
}
