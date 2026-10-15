// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"errors"
	"testing"
)

// "Sync the probes with Zabbix" prunes what Zabbix no longer lists, but an empty answer (a token
// that may not see proxies answers [] and no error) must not take the whole fleet's credentials.
func TestReconcileProxiesRefusesAnEmptyAnswer(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, n := range []string{"proxy-site1", "proxy-site2"} {
		if err := st.UpsertProbeCredential(ctx, n, "hash-"+n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ReconcileProxies(ctx, map[string]bool{}, map[string]bool{}); !errors.Is(err, ErrNoProxies) {
		t.Fatalf("empty answer: err = %v, want ErrNoProxies", err)
	}
	if _, err := st.ProbeNameByToken(ctx, "hash-proxy-site1"); err != nil {
		t.Fatalf("a credential was pruned on an empty answer: %v", err)
	}
	pruned, err := st.ReconcileProxies(ctx, map[string]bool{"proxy-site1": true}, map[string]bool{"1": true})
	if err != nil || pruned != 1 {
		t.Fatalf("real answer: pruned %d, err %v", pruned, err)
	}
	if _, err := st.ProbeNameByToken(ctx, "hash-proxy-site2"); err == nil {
		t.Fatal("the gone proxy's credential survived")
	}
}
