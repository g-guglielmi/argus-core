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
	"argus/internal/zabbix"
)

// A field kit's hosts (DESIGN 18): while the kit is away its being offline doesn't alert (the
// notifier, kitAwayMuted) and its hosts are held by its site's probe, as any probe's are. Once it
// has been away the Field kit hosts kept for days (Settings, Probes; 0 = never), the hosts it
// monitors are removed, from Zabbix and from Argus; its own Probe host stays. An admin can remove
// them at once from its Field kit popup, before taking it to the next network.

const kitExpiryInterval = time.Hour

// kitRemovals are the removals the expiry made, waiting for the notices loop to tell them.
type kitRemovals struct {
	mu   sync.Mutex
	list []notice
}

func (k *kitRemovals) add(n notice) { k.mu.Lock(); k.list = append(k.list, n); k.mu.Unlock() }

func (k *kitRemovals) take() []notice {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := k.list
	k.list = nil
	return out
}

// kitHosts are the hosts a kit's proxy monitors, but its own Probe host.
func kitHosts(proxy zabbix.Proxy, hosts []zabbix.Host) []zabbix.Host {
	own := probeHostName(proxy.Name)
	var out []zabbix.Host
	for _, h := range hosts {
		if h.ProxyID == proxy.ProxyID && h.Name != own {
			out = append(out, h)
		}
	}
	return out
}

// kitAway says whether a kit's proxy has been away longer than days: seen once, not since.
func kitAway(proxy zabbix.Proxy, days int, now time.Time) bool {
	la := atoi64(proxy.LastAccess)
	return days > 0 && la > 0 && now.Unix()-la > int64(days)*86400
}

// proxyByName finds a proxy in Zabbix by name.
func (s *Server) proxyByName(ctx context.Context, name string) (zabbix.Proxy, error) {
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return zabbix.Proxy{}, err
	}
	for _, p := range proxies {
		if p.Name == name {
			return p, nil
		}
	}
	return zabbix.Proxy{}, store.ErrNotFound
}

// removeKitHosts removes the hosts a kit monitors, from Zabbix and from Argus. It returns their ids
// and names.
func (s *Server) removeKitHosts(ctx context.Context, proxy zabbix.Proxy, why string) ([]string, []string, error) {
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	gone := kitHosts(proxy, hosts)
	if len(gone) == 0 {
		return nil, nil, nil
	}
	ids := make([]string, len(gone))
	names := make([]string, len(gone))
	for i, h := range gone {
		ids[i], names[i] = h.HostID, h.Name
	}
	if err := s.zbx.DeleteHost(ctx, ids...); err != nil {
		return nil, nil, err
	}
	if err := s.st.DeleteHostRecords(ctx, ids); err != nil {
		s.logger.Warn("field kit: hosts removed from Zabbix, Argus's records of them stay", "proxy", proxy.Name, "err", err)
	}
	s.logger.Info("field kit: hosts removed", "proxy", proxy.Name, "hosts", len(names), "why", why)
	return ids, names, nil
}

// startKitExpiry removes, once an hour, the hosts of the field kits away longer than the setting.
func (s *Server) startKitExpiry(ctx context.Context) {
	go func() {
		first := time.NewTimer(5 * time.Minute)
		defer first.Stop()
		t := time.NewTicker(kitExpiryInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			s.kitExpiryTick(c, time.Now())
			cancel()
		}
	}()
}

func (s *Server) kitExpiryTick(ctx context.Context, now time.Time) {
	days := s.mgr.KitHostsDays()
	if days <= 0 || !s.zbx.Authenticated() {
		return
	}
	kits, err := s.st.ProbeKits(ctx)
	if err != nil || len(kits) == 0 {
		return
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return
	}
	for _, p := range proxies {
		if !kits[p.Name] || !kitAway(p, days, now) {
			continue
		}
		why := fmt.Sprintf("away %d days", days)
		ids, names, err := s.removeKitHosts(ctx, p, why)
		if err != nil {
			s.logger.Warn("field kit: could not remove the hosts", "proxy", p.Name, "err", err)
			continue
		}
		if len(names) > 0 {
			s.logArgusChange(ctx, store.Change{Category: "probes", Action: "Removed a field kit's hosts", Object: p.Name,
				Detail: fmt.Sprintf("%d %s, %s", len(names), plural(len(names), "host", "hosts"), why), HostIDs: ids})
			s.kitGone.add(notice{
				key:    fmt.Sprintf("kit-hosts:%s:%d", p.Name, now.Unix()),
				title:  fmt.Sprintf("Field kit %s: its %d %s removed, away %d days", p.Name, len(names), plural(len(names), "host", "hosts"), days),
				detail: "Removed: " + joinMax(names, 10) + ". Its incidents and alerts stay in the history.",
				site:   probeSite(p.Name), view: "probes",
			})
		}
	}
}

// GET /api/probes/{name}/kit-hosts (admin): how many hosts the kit monitors, and when they go.
// POST /api/probes/{name}/kit-hosts/remove: removes them now (a kit only).
func (s *Server) handleKitHosts(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	proxy, err := s.proxyByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such probe"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not read the probes from Zabbix"})
		return
	}
	if r.Method == http.MethodGet {
		hosts, err := s.zbx.Hosts(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not read the hosts from Zabbix"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"hosts": len(kitHosts(proxy, hosts)), "days": s.mgr.KitHostsDays(), "last_access": atoi64(proxy.LastAccess)})
		return
	}
	if kit, _ := s.st.ProbeIsKit(ctx, name); !kit {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this probe isn't a field kit: its hosts stay"})
		return
	}
	_, names, err := s.removeKitHosts(ctx, proxy, "removed by hand")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not remove the hosts: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": len(names)})
}

// joinMax lists up to max names, then how many more.
func joinMax(names []string, max int) string {
	if len(names) <= max {
		return joinComma(names)
	}
	return fmt.Sprintf("%s and %d more", joinComma(names[:max]), len(names)-max)
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// kitProbeHosts are the Probe hosts of the field kits (by host id), whose not reporting the
// notifier never alerts: a kit away is expected. Nothing when there is no kit (no extra call).
func kitProbeHosts(ctx context.Context, st *store.Store, zbx *zabbix.Client, m masterSet) map[string]bool {
	kits, err := st.ProbeKits(ctx)
	if err != nil || len(kits) == 0 {
		return nil
	}
	proxies, err := zbx.Proxies(ctx)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, p := range proxies {
		if hostID, ok := m.probeHost[p.ProxyID]; ok && kits[p.Name] {
			out[hostID] = true
		}
	}
	return out
}
