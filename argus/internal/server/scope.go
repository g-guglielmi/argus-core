// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Per-site visibility. A helpdesk or viewer account can be limited to some sites: host groups, where
// a root covers its subgroups (the same rule as a notification channel's sites). Such a user sees
// only the hosts in those groups, their sensors, problems, incidents and charts, and can act only on
// them; everything else answers as if it didn't exist. An admin always sees every site, and an empty
// list means every site. The filter runs on the server for every request: the UI only hides what the
// API already refuses. DESIGN section 10.

// siteScope is what one request may see.
type siteScope struct {
	all   bool
	sites []string
}

// scopeOf is a user's scope: every site for an admin or an account with no sites set.
func scopeOf(u *store.User) siteScope {
	if u == nil || u.Role == "admin" || len(u.Sites) == 0 {
		return siteScope{all: true}
	}
	return siteScope{sites: u.Sites}
}

// scopeFrom is the signed-in user's scope. A request without a user (the signed alert link, the
// status pages) never reaches a scoped handler.
func scopeFrom(r *http.Request) siteScope {
	u, _ := auth.UserFrom(r.Context())
	return scopeOf(u)
}

// coversGroup reports whether group g is inside the scope (one of its sites, or below one).
func (sc siteScope) coversGroup(g string) bool {
	if sc.all {
		return true
	}
	for _, s := range sc.sites {
		if siteCovers(s, g) {
			return true
		}
	}
	return false
}

// sees reports whether a host in these groups is visible: any of its groups inside the scope.
func (sc siteScope) sees(groups []string) bool {
	if sc.all {
		return true
	}
	for _, g := range groups {
		if sc.coversGroup(g) {
			return true
		}
	}
	return false
}

// showsGroup reports whether the tree may show group g: inside the scope, or on the path down to one
// of its sites ("site1" when the scope is "site1/Network").
func (sc siteScope) showsGroup(g string) bool {
	if sc.coversGroup(g) {
		return true
	}
	for _, s := range sc.sites {
		if strings.HasPrefix(s, g+"/") {
			return true
		}
	}
	return false
}

// narrow limits a list of sites (a personal channel's) to the scope: a site inside the scope stays,
// one that contains a scope site becomes that scope site, anything else goes. An empty list (every
// site) becomes the scope itself. nil back means "every site" (an unscoped user); an empty non-nil
// slice means none at all.
func (sc siteScope) narrow(sites []string) []string {
	if sc.all {
		return sites
	}
	if len(sites) == 0 {
		return append([]string{}, sc.sites...)
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, c := range sites {
		for _, s := range sc.sites {
			switch {
			case siteCovers(s, c):
				add(c)
			case siteCovers(c, s):
				add(s)
			}
		}
	}
	return out
}

// hostGroupsCache keeps every host's group names for a short while: the scope checks run on most
// requests of a scoped user, and one host.get per request would be wasteful.
type hostGroupsCache struct {
	mu    sync.Mutex
	at    time.Time
	m     map[string][]string
	hosts map[string]hostInfo
}

// hostInfo is what the lists' filters and the change log need to know of a host.
type hostInfo struct {
	Name    string
	ProxyID string // "0" = the server
	Groups  []string
}

const hostGroupsTTL = 30 * time.Second

// hostGroupMap returns host id -> its group names.
func (s *Server) hostGroupMap(ctx context.Context) (map[string][]string, error) {
	if err := s.loadHostIndex(ctx); err != nil {
		return nil, err
	}
	s.hostGroups.mu.Lock()
	defer s.hostGroups.mu.Unlock()
	return s.hostGroups.m, nil
}

// hostIndex returns host id -> its name, probe and groups (the same short-lived cache).
func (s *Server) hostIndex(ctx context.Context) (map[string]hostInfo, error) {
	if err := s.loadHostIndex(ctx); err != nil {
		return nil, err
	}
	s.hostGroups.mu.Lock()
	defer s.hostGroups.mu.Unlock()
	return s.hostGroups.hosts, nil
}

func (s *Server) loadHostIndex(ctx context.Context) error {
	s.hostGroups.mu.Lock()
	defer s.hostGroups.mu.Unlock()
	if s.hostGroups.m != nil && time.Since(s.hostGroups.at) < hostGroupsTTL {
		return nil
	}
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return err
	}
	m := make(map[string][]string, len(hosts))
	idx := make(map[string]hostInfo, len(hosts))
	for _, h := range hosts {
		gs := make([]string, 0, len(h.Groups))
		for _, g := range h.Groups {
			gs = append(gs, g.Name)
		}
		m[h.HostID] = gs
		proxy := h.ProxyID
		if proxy == "" {
			proxy = "0"
		}
		idx[h.HostID] = hostInfo{Name: h.Name, ProxyID: proxy, Groups: gs}
	}
	s.hostGroups.m, s.hostGroups.hosts, s.hostGroups.at = m, idx, time.Now()
	return nil
}

// forgetHostIndex drops the cached host index, so a write that changed hosts is seen at once.
func (s *Server) forgetHostIndex() {
	s.hostGroups.mu.Lock()
	s.hostGroups.at = time.Time{}
	s.hostGroups.mu.Unlock()
}

// visibleHosts is the set of host ids the scope sees; nil when it sees every host.
func (s *Server) visibleHosts(ctx context.Context, sc siteScope) (map[string]bool, error) {
	if sc.all {
		return nil, nil
	}
	m, err := s.hostGroupMap(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, gs := range m {
		if sc.sees(gs) {
			out[id] = true
		}
	}
	return out, nil
}

// visibleHostIDs is visibleHosts as a list, for the Zabbix calls that take host ids.
func visibleHostIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// hostInScope reports whether the scope sees a host. A lookup failure refuses.
func (s *Server) hostInScope(ctx context.Context, sc siteScope, hostID string) bool {
	if sc.all {
		return true
	}
	m, err := s.hostGroupMap(ctx)
	if err != nil {
		return false
	}
	gs, ok := m[hostID]
	return ok && sc.sees(gs)
}

// itemsInScope reports which of these sensors the scope sees (by their hosts); nil when it sees all.
func (s *Server) itemsInScope(ctx context.Context, sc siteScope, itemIDs []string) (map[string]bool, error) {
	if sc.all {
		return nil, nil
	}
	items, err := s.zbx.ItemsByIDs(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	vis, err := s.visibleHosts(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, it := range items {
		if vis[it.HostID] {
			out[id] = true
		}
	}
	return out, nil
}

// eventHostIDs resolves the hosts an event (a Zabbix problem, or one Argus raised itself) is on: from
// the census when it knows the event, else from Zabbix.
func (s *Server) eventHostIDs(ctx context.Context, eventID string) []string {
	if s.census == nil {
		// a Server assembled by hand (tests) has no census: ask Zabbix
	} else if snap, err := s.census.get(ctx); err == nil {
		var out []string
		for _, r := range snap.Rows {
			for _, e := range r.EventIDs {
				if e == eventID {
					out = append(out, r.HostID)
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	switch {
	case strings.HasPrefix(eventID, synthUnsupported):
		if it, err := s.zbx.Item(ctx, strings.TrimPrefix(eventID, synthUnsupported)); err == nil && it != nil {
			return []string{it.HostID}
		}
		return nil
	case strings.HasPrefix(eventID, synthInterface):
		if h, err := s.zbx.InterfaceHostID(ctx, strings.TrimPrefix(eventID, synthInterface)); err == nil && h != "" {
			return []string{h}
		}
		return nil
	}
	m, err := s.zbx.EventHostIDs(ctx, []string{eventID})
	if err != nil {
		return nil
	}
	return m[eventID]
}

// eventInScope reports whether the scope sees an event: every host it is on (an event over hosts in
// two sites belongs to both, so acting on it needs both).
func (s *Server) eventInScope(ctx context.Context, sc siteScope, eventID string) bool {
	if sc.all {
		return true
	}
	hosts := s.eventHostIDs(ctx, eventID)
	if len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		if !s.hostInScope(ctx, sc, h) {
			return false
		}
	}
	return true
}

// proxyInScope reports whether the scope sees a probe: its site is inside the scope.
func (s *Server) proxyInScope(ctx context.Context, sc siteScope, proxyID string) bool {
	if sc.all {
		return true
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return false
	}
	for _, p := range proxies {
		if p.ProxyID == proxyID {
			return sc.coversGroup(probeSite(p.Name))
		}
	}
	return false
}

// scopedItemIDs keeps the sensor ids the scope sees, in order (sparklines, daily bars).
func (s *Server) scopedItemIDs(ctx context.Context, sc siteScope, ids []string) ([]string, error) {
	if sc.all {
		return ids, nil
	}
	vis, err := s.itemsInScope(ctx, sc, ids)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if vis[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// outsideSites is why a site list reaches beyond the scope ("" when it doesn't): a scoped user's
// personal channel picks among their own sites (empty, "all my sites", is fine).
func outsideSites(sc siteScope, sites []string) string {
	if sc.all {
		return ""
	}
	for _, s := range sites {
		if !sc.coversGroup(s) {
			return s + " is not one of your sites"
		}
	}
	return ""
}

// proxyOutOfScope refuses (403) moving a host onto a probe outside a scoped user's sites, and reports
// whether it did. Monitoring by the core server stays open to everyone.
func (s *Server) proxyOutOfScope(w http.ResponseWriter, r *http.Request, monitoredBy int, proxyID string) bool {
	sc := scopeFrom(r)
	if sc.all || monitoredBy != 1 {
		return false
	}
	ctx, cancel := scopeLookupCtx(r)
	defer cancel()
	if s.proxyInScope(ctx, sc, strings.TrimSpace(proxyID)) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "that probe is not at one of your sites"})
	return true
}

func scopeLookupCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// scopedHost guards a route whose {id} is a host: out of scope answers 404, as a missing host would.
func (s *Server) scopedHost(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			ok := s.hostInScope(ctx, sc, r.PathValue("id"))
			cancel()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
				return
			}
		}
		h(w, r)
	}
}

// scopedItem guards a route whose {id} is a sensor (its host must be in scope).
func (s *Server) scopedItem(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			vis, err := s.itemsInScope(ctx, sc, []string{r.PathValue("id")})
			cancel()
			if err != nil || !vis[r.PathValue("id")] {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "sensor not found"})
				return
			}
		}
		h(w, r)
	}
}

// scopedEvent guards a route whose {id} is an event (every host it is on must be in scope).
func (s *Server) scopedEvent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			ok := s.eventInScope(ctx, sc, r.PathValue("id"))
			cancel()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "problem not found"})
				return
			}
		}
		h(w, r)
	}
}

// scopedProxy guards a route whose {id} is a probe (its site must be in scope).
func (s *Server) scopedProxy(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc := scopeFrom(r); !sc.all {
			ctx, cancel := scopeLookupCtx(r)
			ok := s.proxyInScope(ctx, sc, r.PathValue("id"))
			cancel()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe not found"})
				return
			}
		}
		h(w, r)
	}
}

// scopedHostGroups is the host groups a scoped user may see (the tree's groups): inside the scope or on
// the path down to it.
func scopedHostGroups(sc siteScope, groups []zabbix.HostGroup) []zabbix.HostGroup {
	if sc.all {
		return groups
	}
	out := make([]zabbix.HostGroup, 0, len(groups))
	for _, g := range groups {
		if sc.showsGroup(g.Name) {
			out = append(out, g)
		}
	}
	return out
}
