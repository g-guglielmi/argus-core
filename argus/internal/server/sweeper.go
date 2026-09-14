// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"log/slog"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// StartExpirySweeper periodically re-enables timed Pauses in Zabbix once their expiry passes,
// and cleans up expired hide/ack rows. Runs until ctx is cancelled.
func StartExpirySweeper(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepOnce(ctx, st, zbx, logger)
		}
	}
}

func sweepOnce(ctx context.Context, st *store.Store, zbx *zabbix.Client, logger *slog.Logger) {
	expired, err := st.ExpiredPauses(ctx)
	if err != nil {
		logger.Warn("sweeper: list expired pauses", "err", err)
		return
	}
	for _, sp := range expired {
		if zbx.Authenticated() {
			var e error
			if sp.Scope == "host" {
				e = zbx.SetHostEnabled(ctx, sp.TargetID, true)
			} else {
				e = zbx.SetItemEnabled(ctx, sp.TargetID, true)
			}
			if e != nil {
				logger.Warn("sweeper: re-enable failed; will retry next tick", "scope", sp.Scope, "id", sp.TargetID, "err", e)
				continue // keep the pause row so we retry
			}
		}
		_ = st.ClearSuppression(ctx, "pause", sp.Scope, sp.TargetID)
	}
	_ = st.DeleteExpiredNonPause(ctx)
}
