// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"argus/internal/store"
)

// DELETE /api/hosts/{id} (admin): deletes a host from Zabbix, its sensors and their history with it,
// and forgets what Argus keeps on it and its sensors (settings, sensor states, runbooks, actions
// aimed at it, uptime targets, its places in saved reports). The history Argus keeps (incidents,
// alerts sent, the change log) stays. A probe's own host goes with its probe, not from here.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.deleteHost(ctx, r.PathValue("id")); err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errProbeHost) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": s.errText(r, err)})
		return
	}
	s.forgetHostIndex()
	if s.census != nil {
		s.census.invalidate()
	}
	w.WriteHeader(http.StatusNoContent)
}

// deletedHosts marks the hosts deleted lately (id -> when). Zabbix drops a deleted host's problems
// with it, which would read as recoveries: a notifier pass that loaded their alerts before the host
// went closes them without a RESOLVED (notifyTick).
var deletedHosts sync.Map

// deletedMarkKeep is how long a deleted host stays marked: well past any notifier pass that started
// before it went.
const deletedMarkKeep = 15 * time.Minute

func markHostDeleted(id string, now time.Time) {
	deletedHosts.Range(func(k, v any) bool {
		if now.Sub(v.(time.Time)) > deletedMarkKeep {
			deletedHosts.Delete(k)
		}
		return true
	})
	deletedHosts.Store(id, now)
}

func hostDeleted(id string) bool {
	v, ok := deletedHosts.Load(id)
	return ok && time.Since(v.(time.Time)) <= deletedMarkKeep
}

// closeDeletedAlert notes on an alert's timeline that it closed with its host, nothing sent.
func closeDeletedAlert(ctx context.Context, st *store.Store, eventID string) {
	tlog(ctx, st, eventID, "deleted", "", "", "nothing was sent", 0)
	forgetTimelineState(eventID)
}

// errProbeHost refuses to delete a probe's own host here.
var errProbeHost = errors.New("a probe's own host goes with its probe: delete the probe under Probes")

// deleteHost deletes one host (handleDeleteHost, the bulk delete): from Zabbix, then what Argus
// keeps on it.
func (s *Server) deleteHost(ctx context.Context, id string) error {
	if class, ok, _ := s.st.GetDeviceClass(ctx, id); ok && class == "probe" {
		return errProbeHost
	}
	items, err := s.zbx.Items(ctx, id)
	if err != nil {
		return fmt.Errorf("Zabbix: %w", err)
	}
	itemIDs := make([]string, 0, len(items))
	ofHost := map[string]bool{}
	for _, it := range items {
		itemIDs = append(itemIDs, it.ItemID)
		ofHost[it.ItemID] = true
	}
	markHostDeleted(id, time.Now())
	if err := s.zbx.DeleteHost(ctx, id); err != nil {
		deletedHosts.Delete(id)
		return fmt.Errorf("Zabbix could not delete the host: %w", err)
	}
	// Its open alerts close with it, without a RESOLVED: nothing came back.
	if ids, err := s.st.ForgetHostAlerts(ctx, id); err != nil {
		s.logger.Warn("delete host: could not close its alerts", "host", id, "err", err)
	} else {
		for _, eid := range ids {
			closeDeletedAlert(ctx, s.st, eid)
		}
	}
	// Actions aimed at the host or one of its sensors have nothing left to run on.
	if acts, err := s.st.Actions(ctx); err == nil {
		for _, a := range acts {
			if a.HostID == id || (a.ItemID != "" && ofHost[a.ItemID]) {
				if err := s.removeAction(ctx, a); err != nil {
					s.logger.Warn("delete host: could not delete an action aimed at it", "action", a.ID, "err", err)
				}
			}
		}
	}
	if err := s.st.ForgetDeletedHosts(ctx, []string{id}, itemIDs); err != nil {
		s.logger.Warn("delete host: the host is gone from Zabbix, some of Argus's records of it stay", "host", id, "err", err)
	}
	forgetRunbooks()
	s.logger.Info("host deleted", "host", id, "sensors", len(itemIDs))
	return nil
}
