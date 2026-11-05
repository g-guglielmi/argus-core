// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestProbeKitFlag(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetProbeKit(ctx, "proxy-kit1", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a probe with no credential: %v, want ErrNotFound", err)
	}
	if err := st.UpsertProbeCredential(ctx, "proxy-kit1", "h1"); err != nil {
		t.Fatal(err)
	}
	if kit, _ := st.ProbeIsKit(ctx, "proxy-kit1"); kit {
		t.Fatal("a new probe is a kit")
	}
	if err := st.SetProbeKit(ctx, "proxy-kit1", true); err != nil {
		t.Fatal(err)
	}
	// A new credential (re-enrollment) keeps the switch: it is the admin's word on the device.
	if err := st.UpsertProbeCredential(ctx, "proxy-kit1", "h2"); err != nil {
		t.Fatal(err)
	}
	kits, err := st.ProbeKits(ctx)
	if err != nil || !kits["proxy-kit1"] || len(kits) != 1 {
		t.Fatalf("kits = %v, %v", kits, err)
	}
	if err := st.SetProbeKit(ctx, "proxy-kit1", false); err != nil {
		t.Fatal(err)
	}
	if kit, _ := st.ProbeIsKit(ctx, "proxy-kit1"); kit {
		t.Fatal("switched off, still a kit")
	}
}
