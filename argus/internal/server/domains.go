// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"argus/internal/domainexp"
	"argus/internal/store"
)

// Domain expiry (DESIGN section 7n): when each host's domains stop being registered, beside its
// certificates. A host's domains are the registrable domains its web checks name ({$HTTP.URLS}:
// shop.example.com -> example.com) and the ones typed on it (a domain used only for mail). Argus reads
// each domain once a day (RDAP, else WHOIS: .it has no RDAP) and hands the days left to the host's
// probe with its push sensors (the "Argus Push" template reads both), so a domain is an ordinary
// sensor: a warning under {$DOMAIN.EXPIRY.WARN} days, an error under {$DOMAIN.EXPIRY.HIGH}. A domain
// whose registry publishes no expiry (.de, .eu) gets no sensor, which would only ever read "not
// supported"; host settings say why instead.

const (
	domainEvery      = time.Hour
	domainRecheck    = 24 * time.Hour
	domainRetry      = 6 * time.Hour
	maxDomainsTyped  = 20
	domainMacroURLs  = "{$HTTP.URLS}"
	domainLookupGap  = 2 * time.Second // between lookups: registries rate-limit
	domainLookupWait = 45 * time.Second
)

var reURLListSep = regexp.MustCompile(`[,\s]+`)

// urlDomains is the registrable domains a web check list names (full URLs and bare host names; a path
// checks the host's own address).
func urlDomains(list string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range reURLListSep.Split(strings.TrimSpace(list), -1) {
		e, _, _ = strings.Cut(e, "#")
		if e == "" || strings.HasPrefix(e, "/") {
			continue
		}
		if !strings.Contains(e, "://") {
			e = "https://" + e
		}
		u, err := url.Parse(e)
		if err != nil {
			continue
		}
		if d := domainexp.Registrable(u.Hostname()); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// domainSetsCache keeps the hosts' domain sets a few minutes: the push poll reads them every minute
// for every host with push sensors.
var domainSetsCache struct {
	sync.Mutex
	at          time.Time
	auto, extra map[string][]string
}

const domainSetsTTL = 5 * time.Minute

func forgetDomainSets() {
	domainSetsCache.Lock()
	domainSetsCache.at = time.Time{}
	domainSetsCache.Unlock()
}

// hostDomainSets are each host's domains: from its web checks (auto) and typed on it (extra).
func (s *Server) hostDomainSets(ctx context.Context) (auto, extra map[string][]string, err error) {
	if s.zbx == nil || !s.zbx.Authenticated() {
		return map[string][]string{}, map[string][]string{}, nil
	}
	domainSetsCache.Lock()
	if !domainSetsCache.at.IsZero() && time.Since(domainSetsCache.at) < domainSetsTTL {
		a, e := domainSetsCache.auto, domainSetsCache.extra
		domainSetsCache.Unlock()
		return a, e, nil
	}
	domainSetsCache.Unlock()
	auto, extra, err = s.readDomainSets(ctx)
	if err == nil {
		domainSetsCache.Lock()
		domainSetsCache.at, domainSetsCache.auto, domainSetsCache.extra = time.Now(), auto, extra
		domainSetsCache.Unlock()
	}
	return auto, extra, err
}

func (s *Server) readDomainSets(ctx context.Context) (auto, extra map[string][]string, err error) {
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	auto = map[string][]string{}
	if m, err := s.zbx.UserMacroHosts(ctx, domainMacroURLs); err == nil {
		for host, list := range m {
			if _, real := idx[host]; real { // not the template's own default
				if ds := urlDomains(list); len(ds) > 0 {
					auto[host] = ds
				}
			}
		}
	}
	extra, err = s.st.HostDomains(ctx)
	if err != nil {
		return nil, nil, err
	}
	return auto, extra, nil
}

func unionDomains(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append(append([]string{}, a...), b...) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// startDomainChecks reads the domains that are due once an hour, and keeps the push template on the
// hosts that have domains.
func (s *Server) startDomainChecks(ctx context.Context) {
	go func() {
		t := time.NewTimer(90 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.domainKick:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
			}
			if s.zbx.Authenticated() {
				s.checkDomains(ctx)
			}
			t.Reset(domainEvery)
		}
	}()
}

func (s *Server) wakeDomainChecks() {
	select {
	case s.domainKick <- struct{}{}:
	default:
	}
}

func (s *Server) checkDomains(ctx context.Context) {
	forgetDomainSets() // the hourly read sees web checks changed since
	auto, extra, err := s.hostDomainSets(ctx)
	if err != nil {
		return
	}
	want := map[string]bool{}
	hosts := s.targetedHosts(ctx) // an uptime target reaches the probe the same way
	for _, m := range []map[string][]string{auto, extra} {
		for h, ds := range m {
			hosts[h] = true
			for _, d := range ds {
				want[d] = true
			}
		}
	}
	// The push template carries the domains to the host's probe: on with its first domain, off with
	// its last when it has no push sensor either.
	if base := strings.TrimRight(s.mgr.PublicURL(), "/"); base != "" {
		for h := range hosts {
			if _, err := s.st.PushHostKeyHash(ctx, h); err != nil {
				lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.ensurePushHostAt(lctx, base, h); err != nil {
					s.logger.Warn("domains: could not give the host its push template", "host", h, "err", err)
				}
				cancel()
			}
		}
	}
	if keyed, err := s.st.PushHostIDs(ctx); err == nil {
		for _, h := range keyed {
			if hosts[h] {
				continue
			}
			if rest, err := s.st.ListPushSensors(ctx, h); err == nil && len(rest) == 0 {
				lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.dropPushHost(lctx, h)
				cancel()
			}
		}
	}
	checks, err := s.st.DomainChecks(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	var due []string
	for d := range want {
		c, ok := checks[d]
		age := now.Sub(time.Unix(c.CheckedAt, 0))
		if !ok || (c.Error == "" && age > domainRecheck) || (c.Error != "" && age > domainRetry) {
			due = append(due, d)
		}
	}
	sort.Strings(due)
	for i, d := range due {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(domainLookupGap):
			}
		}
		lctx, cancel := context.WithTimeout(ctx, domainLookupWait)
		at, via, err := domainexp.Lookup(lctx, d)
		cancel()
		c := store.DomainCheck{Domain: d, CheckedAt: time.Now().Unix(), Via: via}
		if err != nil {
			c.Error = err.Error()
		} else {
			c.ExpiresAt = at.Unix()
		}
		_ = s.st.SaveDomainCheck(ctx, c)
	}
	_ = s.st.PruneDomainChecks(ctx, want)
}

// pushDomainEntry is a domain as the host's template reads it: only the ones whose expiry is known.
type pushDomainEntry struct {
	Name    string `json:"name"`
	Days    int64  `json:"days"`    // whole days left (below 0 once it expired)
	Expires string `json:"expires"` // YYYY-MM-DD
}

// pushDomains is a host's domains for its template.
func (s *Server) pushDomains(ctx context.Context, hostID string, now time.Time) []pushDomainEntry {
	auto, extra, err := s.hostDomainSets(ctx)
	if err != nil {
		return []pushDomainEntry{}
	}
	checks, _ := s.st.DomainChecks(ctx)
	out := []pushDomainEntry{}
	for _, d := range unionDomains(auto[hostID], extra[hostID]) {
		c := checks[d]
		if c.ExpiresAt == 0 {
			continue
		}
		out = append(out, pushDomainEntry{Name: d, Days: daysLeft(c.ExpiresAt, now.Unix()), Expires: time.Unix(c.ExpiresAt, 0).UTC().Format("2006-01-02")})
	}
	return out
}

// daysLeft is the whole days from now to at: 0 on its last day, -1 the day after it.
func daysLeft(at, now int64) int64 {
	left := at - now
	if left < 0 {
		return -((-left + 86399) / 86400)
	}
	return left / 86400
}

// domainView is one of a host's domains as its settings show it.
type domainView struct {
	Name      string `json:"name"`
	Auto      bool   `json:"auto"` // named by its web checks
	Typed     bool   `json:"typed"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	CheckedAt int64  `json:"checked_at,omitempty"`
	Via       string `json:"via,omitempty"`
	Error     string `json:"error,omitempty"`
}

// GET /api/hosts/{id}/domains: the host's domains, where each comes from, and what Argus read.
func (s *Server) handleHostDomains(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	auto, extra, err := s.hostDomainSets(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	checks, _ := s.st.DomainChecks(r.Context())
	isAuto, isTyped := map[string]bool{}, map[string]bool{}
	for _, d := range auto[hostID] {
		isAuto[d] = true
	}
	for _, d := range extra[hostID] {
		isTyped[d] = true
	}
	out := []domainView{}
	for _, d := range unionDomains(auto[hostID], extra[hostID]) {
		c := checks[d]
		out = append(out, domainView{Name: d, Auto: isAuto[d], Typed: isTyped[d], ExpiresAt: c.ExpiresAt, CheckedAt: c.CheckedAt, Via: c.Via, Error: c.Error})
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out, "public_url": s.mgr.PublicURL() != ""})
}

// PUT /api/hosts/{id}/domains (admin, helpdesk): the domains typed on the host; each is saved as the
// domain a registry sells (mail.example.com -> example.com).
func (s *Server) handleSetHostDomains(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	seen := map[string]bool{}
	var keep []string
	for _, d := range req.Domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		reg := domainexp.Registrable(d)
		if reg == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": d + " isn't a domain a registry sells (an internal name or an address has no expiry)"})
			return
		}
		if !seen[reg] {
			seen[reg] = true
			keep = append(keep, reg)
		}
	}
	if len(keep) > maxDomainsTyped {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "up to 20 domains"})
		return
	}
	hostID := r.PathValue("id")
	cur, _ := s.st.HostDomains(r.Context())
	if err := s.st.SetHostDomains(r.Context(), hostID, keep); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	changeDiff(r, "Domains", strings.Join(cur[hostID], ", "), strings.Join(keep, ", "))
	forgetDomainSets()
	s.wakeDomainChecks()
	s.handleHostDomains(w, r)
}
