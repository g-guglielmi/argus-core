// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
)

func TestUniFiControllerCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.SaveUniFiController(ctx, UniFiController{Name: "site1", URL: "https://unifi.example.lan:11443", APIKey: "k-abc"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := st.ListUniFiControllers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != id || !list[0].HasKey || list[0].APIKey != "" {
		t.Fatalf("listing must carry has_key but never the key: %+v", list)
	}

	ctl, err := st.UniFiControllerByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ctl.APIKey != "k-abc" {
		t.Fatalf("ByID must decrypt the key, got %q", ctl.APIKey)
	}

	// An update with a blank key keeps the stored one (the settings convention).
	if _, err := st.SaveUniFiController(ctx, UniFiController{ID: id, Name: "site1-renamed", URL: "https://unifi.example.lan:11443", APIKey: ""}); err != nil {
		t.Fatal(err)
	}
	ctl, _ = st.UniFiControllerByID(ctx, id)
	if ctl.Name != "site1-renamed" || ctl.APIKey != "k-abc" {
		t.Fatalf("blank key on update must keep the stored key: %+v", ctl)
	}

	// A non-blank key replaces it.
	if _, err := st.SaveUniFiController(ctx, UniFiController{ID: id, Name: "site1-renamed", URL: "https://unifi.example.lan:11443", APIKey: "k-new"}); err != nil {
		t.Fatal(err)
	}
	if ctl, _ = st.UniFiControllerByID(ctx, id); ctl.APIKey != "k-new" {
		t.Fatalf("key not replaced: %q", ctl.APIKey)
	}

	if err := st.DeleteUniFiController(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UniFiControllerByID(ctx, id); err != ErrNotFound {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	if err := st.DeleteUniFiController(ctx, id); err != ErrNotFound {
		t.Fatalf("double delete should be ErrNotFound, got %v", err)
	}
	if _, err := st.SaveUniFiController(ctx, UniFiController{ID: 999, Name: "x", URL: "https://x", APIKey: "k"}); err != ErrNotFound {
		t.Fatalf("update of a missing row should be ErrNotFound, got %v", err)
	}
}

// A sweep job travels the same queue as a scan job, carrying its kind and controller reference.
func TestDiscoveryJobSweepKind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-site1", Kind: "unifi", ControllerID: 7, ControllerName: "site1"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.TakeDiscoveryJob(ctx, "proxy-site1")
	if err != nil || job == nil {
		t.Fatalf("take: %v %v", job, err)
	}
	if job.ID != id || job.Kind != "unifi" || job.ControllerID != 7 || job.ControllerName != "site1" || job.CIDR != "" {
		t.Fatalf("sweep job round-trip wrong: %+v", job)
	}

	// Results with unifi facts complete and read back intact.
	res := []DiscoveryResult{{IP: "10.0.0.2", MAC: "aa:bb:cc:00:00:01", TCPPorts: "[]",
		UniFiJSON: `{"name":"sw-rack","model":"US8P60","type":"usw","state":1,"version":"7.1.26","site":"default"}`,
		SuggestedClass: "unifi-switch"}}
	if err := st.CompleteDiscoveryJob(ctx, id, "proxy-site1", "", res); err != nil {
		t.Fatal(err)
	}
	got, err := st.DiscoveryResultsByJob(ctx, id)
	if err != nil || len(got) != 1 {
		t.Fatalf("results: %v %v", got, err)
	}
	if got[0].UniFiJSON == "" || got[0].SuggestedClass != "unifi-switch" {
		t.Fatalf("unifi facts lost: %+v", got[0])
	}
	one, err := st.DiscoveryResultByID(ctx, got[0].ID)
	if err != nil || one.UniFiJSON != got[0].UniFiJSON || one.JobID != id {
		t.Fatalf("DiscoveryResultByID: %+v %v", one, err)
	}

	// The history "new" counts subtract live-monitored IPs; the store side hands out the new-state
	// IPs per job, and an adopted row drops out.
	newIPs, err := st.DiscoveryNewResultIPs(ctx, []int64{id, 9999})
	if err != nil || len(newIPs[id]) != 1 || newIPs[id][0] != "10.0.0.2" {
		t.Fatalf("DiscoveryNewResultIPs: %v %v", newIPs, err)
	}
	if err := st.MarkDiscoveryResultAdded(ctx, got[0].ID, "hid-1"); err != nil {
		t.Fatal(err)
	}
	if newIPs, _ = st.DiscoveryNewResultIPs(ctx, []int64{id}); len(newIPs[id]) != 0 {
		t.Fatalf("added rows must not count as new: %v", newIPs)
	}

	// A default-kind job stays "scan".
	sid, err := st.CreateDiscoveryJob(ctx, DiscoveryJob{ProxyName: "proxy-site1", CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	sjob, err := st.DiscoveryJobByID(ctx, sid)
	if err != nil || sjob.Kind != "scan" {
		t.Fatalf("default kind should be scan: %+v %v", sjob, err)
	}
}
