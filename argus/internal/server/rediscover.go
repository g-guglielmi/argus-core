// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"argus/internal/zabbix"
)

// rediscoverEvery is how soon a host's discovery runs again for a renumbered instance: twice within
// rediscoverGrace, in case the first request reached the probe before its config did.
const rediscoverEvery = 5 * time.Minute

var rediscoverAsked = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// rediscoverRenumbered runs the discovery rules of each host with a renumbered SNMP instance
// (synthSet.rediscover), at most once per rediscoverEvery, instead of leaving its sensors failing
// until the hourly discovery. Failures only log: the rules still run on their own interval.
func rediscoverRenumbered(ctx context.Context, zbx *zabbix.Client, logger *slog.Logger, hosts map[string]bool) {
	for h := range hosts {
		if !rediscoverDue(h, time.Now()) {
			continue
		}
		rules, err := zbx.DiscoveryRules(ctx, h)
		if err == nil && len(rules) > 0 {
			ids := make([]string, 0, len(rules))
			for _, r := range rules {
				ids = append(ids, r.ItemID)
			}
			err = zbx.ExecuteNow(ctx, ids)
		}
		if err != nil {
			logger.Warn("rediscover: could not run discovery for a renumbered instance", "host", h, "err", err)
			continue
		}
		logger.Info("rediscover: an SNMP instance no longer answers at its index; discovery runs now", "host", h, "rules", len(rules))
	}
}

// rediscoverDue reports whether a host's discovery may run now, and if so marks it run.
func rediscoverDue(host string, now time.Time) bool {
	rediscoverAsked.Lock()
	defer rediscoverAsked.Unlock()
	if last, ok := rediscoverAsked.at[host]; ok && now.Sub(last) < rediscoverEvery {
		return false
	}
	rediscoverAsked.at[host] = now
	return true
}
