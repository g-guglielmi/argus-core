// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"argus/internal/zabbix"
)

// This file is the §C follow-up "discovery trigger": run a host's low-level discovery (LLD) rules on
// demand instead of waiting out their interval (1h on the SNMP classes), so per-instance sensors -
// disks, NICs, ports - appear right after a host is added. Full network auto-discovery stays §B.

// runDiscovery queues a Zabbix "execute now" task for every enabled LLD rule on the host and returns
// the rules it fired (nil when the host has none).
func (s *Server) runDiscovery(ctx context.Context, hostID string) ([]zabbix.DiscoveryRule, error) {
	rules, err := s.zbx.DiscoveryRules(ctx, hostID)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ItemID)
	}
	return rules, s.zbx.ExecuteNow(ctx, ids)
}

// scheduleDiscovery fires a just-created host's discovery rules in the background - delayed, because
// a proxy-monitored host's new config (including the rules) reaches the proxy on its next config
// sync, and a task fired before that can go nowhere. One retry covers a slow sync. Failures only
// log: the rules still run on their own interval regardless.
func (s *Server) scheduleDiscovery(hostID string) {
	go func() {
		for attempt, wait := range []time.Duration{15 * time.Second, 60 * time.Second} {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			rules, err := s.runDiscovery(ctx, hostID)
			cancel()
			if err == nil {
				if len(rules) > 0 {
					s.logger.Info("provision: discovery triggered on new host", "host", hostID, "rules", len(rules))
				}
				return
			}
			s.logger.Warn("provision: discovery trigger failed", "host", hostID, "attempt", attempt+1, "err", err)
		}
	}()
}

// POST /api/hosts/{id}/discover - run the host's discovery rules now (admin/helpdesk; wired in
// server.go). Returns how many rules were fired, and their names, so the UI can say so.
func (s *Server) handleDiscoverNow(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rules, err := s.runDiscovery(ctx, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	names := make([]string, 0, len(rules))
	for _, ru := range rules {
		names = append(names, ru.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggered": len(rules), "rules": names})
}

// POST /api/items/check {item_ids}: run sensors now instead of at their next interval. Each runs the
// item that does its reading (a dependent sensor's master), once however many of its readings are
// asked for: the speed test's download, upload and "ran" are one test.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	var req struct {
		ItemIDs []string `json:"item_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || len(req.ItemIDs) == 0 || len(req.ItemIDs) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "list 1 to 200 sensors"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ids, err := s.scopedItemIDs(ctx, scopeFrom(r), req.ItemIDs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
		return
	}
	roots, err := s.zbx.ReadingItems(ctx, ids)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	seen := map[string]bool{}
	var run []string
	for _, id := range ids {
		if root := roots[id]; root != "" && !seen[root] {
			seen[root] = true
			run = append(run, root)
		}
	}
	if err := s.zbx.ExecuteNow(ctx, run); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix can't run it now: " + s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"checks": len(run)})
}
