// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"testing"
	"time"
)

// An action saves, rewrites and goes; its runs give each action's last run and its last run per host.
func TestActionsStore(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.SaveAction(ctx, Action{Name: "Restart", Type: "service", Via: "ssh", Scope: "kind", ClassID: "linux-ssh", KeyKind: "linux.ssh.unit.active[*]", Config: "{}", CooldownMin: 10})
	if err != nil || id == 0 {
		t.Fatalf("save: %d %v", id, err)
	}
	if err := st.SetActionScript(ctx, id, "42"); err != nil {
		t.Fatal(err)
	}
	a, err := st.ActionByID(ctx, id)
	if err != nil || a.ScriptID != "42" || a.KeyKind != "linux.ssh.unit.active[*]" {
		t.Fatalf("read: %+v %v", a, err)
	}
	a.Name = "Restart the service"
	if _, err := st.SaveAction(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveAction(ctx, Action{ID: 999, Name: "x", Type: "service", Scope: "item"}); err != ErrNoAction {
		t.Errorf("rewriting a missing action: %v", err)
	}
	now := time.Now().Unix()
	_ = st.AddActionRun(ctx, ActionRun{ActionID: id, HostID: "10", At: now - 100, OK: false, Why: "sudo"})
	_ = st.AddActionRun(ctx, ActionRun{ActionID: id, HostID: "10", At: now - 50, OK: true})
	_ = st.AddActionRun(ctx, ActionRun{ActionID: id, HostID: "11", At: now - 10, OK: false, Why: "down"})
	last, lastOn, err := st.LastActionRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last[id].HostID != "11" || !lastOn[ActionHostKey(id, "10")].OK || lastOn[ActionHostKey(id, "11")].Why != "down" {
		t.Errorf("last runs: %+v %+v", last, lastOn)
	}
	_ = st.PruneActionRuns(ctx, now-60)
	if _, lastOn, _ := st.LastActionRuns(ctx); len(lastOn) != 2 {
		t.Errorf("after prune: %+v", lastOn)
	}
	if err := st.DeleteAction(ctx, id); err != nil {
		t.Fatal(err)
	}
	if as, _ := st.Actions(ctx); len(as) != 0 {
		t.Errorf("left: %+v", as)
	}
}
