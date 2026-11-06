// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"argus/internal/secret"
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

// A token minted by the Raspberry Pi kit method says so when it is claimed.
func TestEnrollTokenKit(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exp := time.Now().Add(time.Hour)
	plain, _ := st.CreateEnrollToken(ctx, "h-plain", "proxy-site1", "site1", 1, exp)
	kit, _ := st.CreateEnrollToken(ctx, "h-kit", "proxy-kit1", "kit1", 1, exp)
	if err := st.SetEnrollTokenKit(ctx, kit); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]bool{"h-plain": false, "h-kit": true} {
		tok, err := st.ClaimEnrollToken(ctx, hash, time.Now())
		if err != nil || tok.Kit != want {
			t.Fatalf("%s: %+v, %v; want kit=%v", hash, tok, err, want)
		}
	}
	_ = plain
}

// A DietPi token's console credential is kept encrypted and handed over once, at enrollment.
func TestEnrollTokenBreakGlass(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _, err := secret.Load("a-test-secret-key-long-enough-1234", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.SetCipher(c)
	exp := time.Now().Add(time.Hour)
	id, _ := st.CreateEnrollToken(ctx, "h-dp", "proxy-dp1", "dp1", 1, exp)
	plain, _ := st.CreateEnrollToken(ctx, "h-plain", "proxy-site1", "site1", 1, exp)
	if err := st.SetEnrollTokenBreakGlass(ctx, id, "root", "Secret23"); err != nil {
		t.Fatal(err)
	}
	var stored string
	_ = st.db.QueryRowContext(ctx, `SELECT bg_secret FROM enroll_tokens WHERE id=?`, id).Scan(&stored)
	if stored == "" || stored == "Secret23" {
		t.Fatalf("kept as %q, want it encrypted", stored)
	}
	if u, p, err := st.TakeEnrollTokenBreakGlass(ctx, id); err != nil || u != "root" || p != "Secret23" {
		t.Fatalf("took %q %q %v", u, p, err)
	}
	if u, p, err := st.TakeEnrollTokenBreakGlass(ctx, id); err != nil || u != "" || p != "" {
		t.Fatalf("a second take gave %q %q %v, want nothing", u, p, err)
	}
	if u, p, err := st.TakeEnrollTokenBreakGlass(ctx, plain); err != nil || u != "" || p != "" {
		t.Fatalf("a plain token gave %q %q %v", u, p, err)
	}
}
