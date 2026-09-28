// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/notify"
	"argus/internal/store"
)

// Status pages: a read-only dashboard for a wall screen, opened with a secret link instead of a login.
//
//   - The link is /status/<token>. Opening it swaps the token for a cookie and redirects to a clean
//     /status, so the token doesn't linger in the address bar, history suggestions or a screenshot.
//     It's looked up by its SHA-256 and kept encrypted (like channel secrets) so an admin can copy the
//     link again; rotating it or deleting the page kills the old link.
//   - A page can be limited to networks (CIDRs) and given an expiry; it shows only the sites it was
//     given, and never addresses, credentials or settings. It reads through /status/data, never the
//     app's API, so the cookie opens nothing else.
//   - The page is its own small HTML file (not the app), refreshing every 30 seconds.

//go:embed statuspage.html
var statusPageHTML []byte

const (
	statusCookie   = "argus_status"
	statusCacheTTL = 20 * time.Second
)

// --- admin API ---

type statusPageView struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Sites        []string `json:"sites"`
	AllowCIDRs   string   `json:"allow_cidrs"`
	ExpiresAt    int64    `json:"expires_at"`
	CreatedAt    int64    `json:"created_at"`
	CreatedBy    string   `json:"created_by"`
	LastViewedAt int64    `json:"last_viewed_at"`
	HasLink      bool     `json:"has_link"` // the link can be copied again
}

func toStatusPageView(p store.StatusPage) statusPageView {
	sites := p.Sites
	if sites == nil {
		sites = []string{}
	}
	return statusPageView{ID: p.ID, Name: p.Name, Sites: sites, AllowCIDRs: p.AllowCIDRs, ExpiresAt: p.ExpiresAt,
		CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy, LastViewedAt: p.LastViewedAt, HasLink: p.HasLink}
}

type statusPageRequest struct {
	Name       string   `json:"name"`
	Sites      []string `json:"sites"`
	AllowCIDRs string   `json:"allow_cidrs"`
	ExpiresAt  int64    `json:"expires_at"` // unix s; 0 = never
}

func (req statusPageRequest) validate() (store.StatusPage, string) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return store.StatusPage{}, "a name is required"
	}
	cidrs, msg := normalizeCIDRs(req.AllowCIDRs)
	if msg != "" {
		return store.StatusPage{}, msg
	}
	if req.ExpiresAt < 0 || (req.ExpiresAt > 0 && req.ExpiresAt <= time.Now().Unix()) {
		return store.StatusPage{}, "the expiry must be in the future"
	}
	return store.StatusPage{Name: name, Sites: cleanSites(req.Sites), AllowCIDRs: cidrs, ExpiresAt: req.ExpiresAt}, ""
}

// normalizeCIDRs checks a comma- or space-separated list of networks ("10.0.0.0/24, 192.168.1.5") and
// returns it canonical, a bare address counting as that one host.
func normalizeCIDRs(raw string) (string, string) {
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked().String())
			continue
		}
		if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
			continue
		}
		return "", "\"" + f + "\" isn't a network (like 10.0.0.0/24) or an address"
	}
	return strings.Join(out, ", "), ""
}

// cidrAllows reports whether ip is inside one of the networks ("" allows everyone).
func cidrAllows(cidrs, ip string) bool {
	if strings.TrimSpace(cidrs) == "" {
		return true
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, f := range strings.Split(cidrs, ",") {
		if p, err := netip.ParsePrefix(strings.TrimSpace(f)); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

func newStatusToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// statusLink is a page's link: absolute with a public URL, else the path (the UI prefixes its origin).
func (s *Server) statusLink(token string) string {
	return strings.TrimRight(s.mgr.PublicURL(), "/") + "/status/" + token
}

func (s *Server) handleListStatusPages(w http.ResponseWriter, r *http.Request) {
	pages, err := s.st.ListStatusPages(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	out := make([]statusPageView, 0, len(pages))
	for _, p := range pages {
		out = append(out, toStatusPageView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateStatusPage(w http.ResponseWriter, r *http.Request) {
	var req statusPageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	p, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if caller, _ := auth.UserFrom(r.Context()); caller != nil {
		p.CreatedBy = caller.Email
	}
	token := newStatusToken()
	id, err := s.st.CreateStatusPage(r.Context(), p, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	created, _ := s.st.GetStatusPage(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"page": toStatusPageView(*created), "link": s.statusLink(token)})
}

func (s *Server) handleUpdateStatusPage(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if _, err := s.st.GetStatusPage(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "status page not found"})
		return
	}
	var req statusPageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	p, msg := req.validate()
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	p.ID = id
	if err := s.st.UpdateStatusPage(r.Context(), p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	statusData.forget(id)
	updated, _ := s.st.GetStatusPage(r.Context(), id)
	writeJSON(w, http.StatusOK, toStatusPageView(*updated))
}

// POST /api/status-pages/{id}/rotate - a new link; the old one stops working at once.
func (s *Server) handleRotateStatusPage(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if _, err := s.st.GetStatusPage(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "status page not found"})
		return
	}
	token := newStatusToken()
	if err := s.st.RotateStatusPageToken(r.Context(), id, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"link": s.statusLink(token)})
}

// GET /api/status-pages/{id}/link - the page's current link, to copy it again (admin).
func (s *Server) handleStatusPageLink(w http.ResponseWriter, r *http.Request) {
	token, err := s.st.StatusPageToken(r.Context(), atoi64(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this page's link isn't kept (it predates copyable links): make a new link"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"link": s.statusLink(token)})
}

func (s *Server) handleDeleteStatusPage(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if err := s.st.DeleteStatusPage(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	statusData.forget(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- the public page ---

// statusHeaders keeps the page out of search engines, other sites' frames, caches and Referer headers.
func statusHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// statusPageFor resolves the page a request may see: by token, then expiry and network. On failure it
// writes the answer (a generic message: nothing about which check failed beyond what the viewer can fix).
func (s *Server) statusPageFor(w http.ResponseWriter, r *http.Request, token string, asJSON bool) *store.StatusPage {
	fail := func(code int, msg string) *store.StatusPage {
		if asJSON {
			writeJSON(w, code, map[string]string{"error": msg})
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(msg + "\n"))
		}
		return nil
	}
	p, err := s.st.StatusPageByToken(r.Context(), token)
	if err != nil {
		return fail(http.StatusNotFound, "This status page link isn't valid. Ask for a new one.")
	}
	if p.ExpiresAt > 0 && time.Now().Unix() >= p.ExpiresAt {
		return fail(http.StatusGone, "This status page link has expired. Ask for a new one.")
	}
	if !cidrAllows(p.AllowCIDRs, s.clientIP(r)) {
		return fail(http.StatusForbidden, "This status page can't be opened from this network.")
	}
	return p
}

// GET /status/{token} - trade the link's token for a cookie, then show the page at a clean address.
func (s *Server) handleStatusLink(w http.ResponseWriter, r *http.Request) {
	statusHeaders(w)
	token := r.PathValue("token")
	p := s.statusPageFor(w, r, token, false)
	if p == nil {
		return
	}
	maxAge := 400 * 24 * 3600 // browsers cap cookies at about 400 days; the TV keeps it
	if p.ExpiresAt > 0 {
		if left := int(p.ExpiresAt - time.Now().Unix()); left < maxAge {
			maxAge = left
		}
	}
	http.SetCookie(w, &http.Cookie{Name: statusCookie, Value: token, Path: "/status", MaxAge: maxAge,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/status", http.StatusSeeOther)
}

func statusTokenFrom(r *http.Request) string {
	if c, err := r.Cookie(statusCookie); err == nil {
		return c.Value
	}
	return ""
}

// GET /status - the page itself (its data comes from /status/data).
func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	statusHeaders(w)
	if s.statusPageFor(w, r, statusTokenFrom(r), false) == nil {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(statusPageHTML)
}

// GET /status/data - what the page shows, for the page the cookie opens.
func (s *Server) handleStatusData(w http.ResponseWriter, r *http.Request) {
	statusHeaders(w)
	p := s.statusPageFor(w, r, statusTokenFrom(r), true)
	if p == nil {
		return
	}
	_ = s.st.TouchStatusPage(r.Context(), p.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	d, err := statusData.get(p.ID, func() (statusView, error) { return s.buildStatus(ctx, *p) })
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "monitoring data unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// --- the data ---

type statusView struct {
	Name        string        `json:"name"`
	GeneratedAt int64         `json:"generated_at"`
	Timezone    string        `json:"timezone"`
	Clock24h    bool          `json:"clock_24h"`
	Counts      statusCounts  `json:"counts"`
	Issues      []statusIssue `json:"issues"`
}

type statusCounts struct {
	Hosts   int `json:"hosts"` // hosts on the page (paused and hidden ones left out)
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Acked   int `json:"acked"`
}

// statusIssue is one row of the list: a sensor that isn't OK - the same rows, in the same order, as
// the app's Overview - with where it is, its reading and recent trend, its priority, and why. Kind is
// the list it belongs to: error | warning | acked.
type statusIssue struct {
	Kind     string    `json:"kind"`
	Host     string    `json:"host"`
	Site     string    `json:"site"`
	Sensor   string    `json:"sensor"`
	Reason   string    `json:"reason,omitempty"`
	Severity int       `json:"severity"`
	Value    string    `json:"value,omitempty"`
	Units    string    `json:"units,omitempty"` // for the trend's scale (percentages keep a 10-point floor)
	Spark    []float64 `json:"spark,omitempty"`
	Priority int       `json:"priority"`
	Since    int64     `json:"since"`
}

var issueRank = map[string]int{"error": 0, "warning": 1, "acked": 2}

// statusSparkMax caps how many rows get a trend line (one history read covers them all).
const statusSparkMax = 200

// buildStatus assembles a page's view from the sensor census - the rows behind the app's pills and
// Overview - limited to the page's sites: every sensor in error, in warning or acknowledged, ordered
// like the Overview (priority, then severity, then host and sensor).
func (s *Server) buildStatus(ctx context.Context, p store.StatusPage) (statusView, error) {
	hosts, err := s.zbx.Hosts(ctx)
	if err != nil {
		return statusView{}, err
	}
	sensors, err := s.sensorCensus(ctx)
	if err != nil {
		return statusView{}, err
	}
	hidden, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")

	v := statusView{Name: p.Name, GeneratedAt: time.Now().Unix(), Timezone: s.mgr.Location().String(), Clock24h: s.mgr.Clock24h(), Issues: []statusIssue{}}
	siteOf := map[string]string{}
	for _, h := range hosts {
		if _, isHidden := hidden[h.HostID]; isHidden || h.Status == "1" {
			continue
		}
		for _, g := range h.Groups {
			if statusCovers(p.Sites, g.Name) {
				siteOf[h.HostID] = g.Name
				v.Counts.Hosts++
				break
			}
		}
	}
	var rows []sensorRow
	for _, sr := range sensors {
		if _, onPage := siteOf[sr.HostID]; !onPage {
			continue
		}
		if _, listed := issueRank[sr.State]; listed {
			rows = append(rows, sr)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.HostName != b.HostName {
			return a.HostName < b.HostName
		}
		return a.Name < b.Name
	})
	var sparkIDs []string
	for _, sr := range rows {
		if sr.Numeric && sr.Supported && !sr.Synthetic && len(sparkIDs) < statusSparkMax {
			sparkIDs = append(sparkIDs, sr.ItemID)
		}
	}
	sparks, _ := s.sparkSeries(ctx, sparkIDs, 2*time.Hour) // best effort: rows just lose their trend
	for _, sr := range rows {
		label := sr.Label
		if label == "" {
			label = sr.Name
		}
		is := statusIssue{Kind: sr.State, Host: sr.HostName, Site: siteOf[sr.HostID], Sensor: label, Reason: sr.Reason,
			Severity: sr.Severity, Priority: sr.Priority, Since: sr.Since, Spark: sparks[sr.ItemID]}
		switch r, ok := reachabilityReading(sr.key, sr.Value); {
		case ok:
			is.Value = r
		case !sr.Supported:
			is.Value = "not supported"
		case sr.Synthetic:
			is.Value = sr.Value
		default:
			is.Value, is.Units = notify.FormatReading(sr.Value, sr.Units), sr.Units
		}
		switch sr.State {
		case "error":
			v.Counts.Error++
		case "warning":
			v.Counts.Warning++
		case "acked":
			v.Counts.Acked++
		}
		v.Issues = append(v.Issues, is)
	}
	return v, nil
}

// statusCovers reports whether a page scoped to sites (empty = all) shows host group g.
func statusCovers(sites []string, g string) bool {
	if len(sites) == 0 {
		return true
	}
	for _, s := range sites {
		if siteCovers(s, g) {
			return true
		}
	}
	return false
}

// statusData caches each page's view briefly: a wall of screens refreshing every 30 s shares one read.
var statusData = &statusCache{m: map[int64]statusEntry{}}

type statusEntry struct {
	at time.Time
	v  statusView
}

type statusCache struct {
	mu sync.Mutex
	m  map[int64]statusEntry
}

func (c *statusCache) get(id int64, build func() (statusView, error)) (statusView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[id]; ok && time.Since(e.at) < statusCacheTTL {
		return e.v, nil
	}
	v, err := build()
	if err != nil {
		return v, err
	}
	c.m[id] = statusEntry{at: time.Now(), v: v}
	return v, nil
}

func (c *statusCache) forget(id int64) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}
