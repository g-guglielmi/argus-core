// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"argus/internal/mailread"
	"argus/internal/provision"
)

// The vendor's own word beside the Common SaaS checks (DESIGN section 5, "Common SaaS"): whether the
// service's vendor has an incident open, read from its public status page, so a problem reads "only
// us, from this site" or "the vendor too". Only the vendor's open incidents count, never its headline:
// a CDN's headline reads "major outage" most days from regional rerouting with no incident open, and a
// long no-impact maintenance reads "maintenance" for weeks. The word is context: it never alerts.

// vendorWord is what a vendor says about its service right now.
type vendorWord struct {
	Service string `json:"service"`          // "GitHub"
	Open    int    `json:"open"`             // incidents open
	Impact  string `json:"impact,omitempty"` // the worst one's: none | minor | major | critical
	Title   string `json:"title,omitempty"`  // the worst one's name
	Since   int64  `json:"since,omitempty"`  // when it began (unix s; 0 = not said)
	Page    string `json:"page"`             // the status page, for people
	Checked int64  `json:"checked"`          // when Argus last read it
	Who     string `json:"who,omitempty"`    // the vendor, when it isn't the service ("Microsoft")
	Detail  string `json:"detail,omitempty"` // the whole of what the vendor says, when Title is cut short
	// Source "tenant": the word is the organisation's own Microsoft 365 service health (Graph), not
	// the vendor's public page.
	Source string `json:"source,omitempty"`
	// Note says why the tenant's service health was not read, when it is set up (the word is then
	// Microsoft's public page).
	Note string `json:"note,omitempty"`
}

const (
	vendorPollEvery = 5 * time.Minute
	vendorKeepFor   = 20 * time.Minute // a word older than this (the page stopped answering) is dropped
	vendorBodyMax   = 1 << 20
)

// vendorCache holds the words, by catalog service, and which service each SaaS sensor checks.
type vendorCache struct {
	mu     sync.Mutex
	words  map[string]vendorWord // service id -> word
	byItem map[string]string     // item id -> service id
}

// vendors is shared by the views and the notifier (which runs apart from the Server).
var vendors = &vendorCache{words: map[string]vendorWord{}, byItem: map[string]string{}}

var vendorClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// saasByURLID maps the id argus_http.py gives a URL (the first 10 hex of its SHA-1) to the catalog
// service checking it, so a renamed service still finds its vendor.
var saasByURLID = func() map[string]provision.SaaSService {
	out := map[string]provision.SaaSService{}
	for _, svc := range provision.SaaSServices() {
		sum := sha1.Sum([]byte(svc.URL))
		out[hex.EncodeToString(sum[:])[:10]] = svc
	}
	return out
}()

// saasServiceOfKey is the catalog service a saas.up[...] / saas.time[...] sensor checks.
func saasServiceOfKey(key string) (provision.SaaSService, bool) {
	for _, p := range []string{"saas.up[", "saas.time["} {
		if strings.HasPrefix(key, p) && strings.HasSuffix(key, "]") {
			svc, ok := saasByURLID[key[len(p):len(key)-1]]
			return svc, ok
		}
	}
	return provision.SaaSService{}, false
}

// wordForKey is the vendor's word for a sensor by its key, if its vendor has one fresh enough.
func (c *vendorCache) wordForKey(key string) *vendorWord {
	svc, ok := saasServiceOfKey(key)
	if !ok || svc.Status == nil {
		return nil
	}
	return c.word(svc.ID)
}

// wordForItem is the same by the sensor's item id (a RESOLVED knows only that).
func (c *vendorCache) wordForItem(itemID string) *vendorWord {
	c.mu.Lock()
	id := c.byItem[itemID]
	c.mu.Unlock()
	if id == "" {
		return nil
	}
	return c.word(id)
}

func (c *vendorCache) word(id string) *vendorWord {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.words[id]
	if !ok || time.Since(time.Unix(w.Checked, 0)) > vendorKeepFor {
		return nil
	}
	return &w
}

// startVendorStatus reads, every few minutes, the status of the vendors whose services some site's
// Common SaaS add-on checks (no add-on, no request).
func (s *Server) startVendorStatus(ctx context.Context) {
	go func() {
		t := time.NewTimer(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if s.zbx.Authenticated() {
				s.pollVendors(ctx)
			}
			t.Reset(vendorPollEvery)
		}
	}()
}

func (s *Server) pollVendors(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	items, err := s.zbx.ItemsByKeyPrefix(lctx, "saas.")
	cancel()
	if err != nil {
		return
	}
	byItem := map[string]string{}
	used := map[string]provision.SaaSService{}
	for _, it := range items {
		if svc, ok := saasServiceOfKey(it.Key); ok && svc.Status != nil {
			byItem[it.ItemID] = svc.ID
			used[svc.ID] = svc
		}
	}
	vendors.mu.Lock()
	vendors.byItem = byItem
	vendors.mu.Unlock()
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	bodies := map[string][]byte{} // one read per source a pass (Outlook, Teams and Microsoft 365 share one)
	for _, id := range ids {
		svc := used[id]
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		w, err := fetchVendorWordShared(fctx, vendorClient, svc, bodies)
		cancel()
		if err != nil {
			s.logger.Debug("vendor status: could not read", "service", svc.Name, "err", err)
			continue
		}
		w.Checked = time.Now().Unix()
		vendors.mu.Lock()
		vendors.words[id] = w
		vendors.mu.Unlock()
	}
	s.pollTenantHealth(ctx, used)
}

// tenantServices names, for each catalog service, the Microsoft 365 services whose issues it shows
// from the tenant's service health (nil = every service: Microsoft 365 stands for the whole suite).
var tenantServices = map[string][]string{
	"m365":    nil,
	"outlook": {"Exchange Online"},
	"teams":   {"Microsoft Teams"},
}

// m365Health is how reading the tenant's service health has gone (Settings shows it).
var m365Health struct {
	sync.Mutex
	ReadAt  int64  `json:"read_at,omitempty"`
	Open    int    `json:"open"`
	Error   string `json:"error,omitempty"`
	ErrorAt int64  `json:"error_at,omitempty"`
}

// readTenantHealth reads the tenant's open issues and records how it went.
func (s *Server) readTenantHealth(ctx context.Context) ([]mailread.ServiceIssue, error) {
	app := s.mgr.M365()
	if !app.Configured() {
		return nil, errors.New("not set up")
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	issues, err := mailread.ServiceIssues(rctx, app.Tenant, app.ClientID, app.Secret)
	m365Health.Lock()
	defer m365Health.Unlock()
	if err != nil {
		m365Health.Error, m365Health.ErrorAt = err.Error(), time.Now().Unix()
		return nil, err
	}
	m365Health.ReadAt, m365Health.Open, m365Health.Error, m365Health.ErrorAt = time.Now().Unix(), countIncidents(issues), "", 0
	return issues, nil
}

// pollTenantHealth puts the tenant's own service health in place of Microsoft's public page on the
// Microsoft services checked, when it is set up; when it can't be read the public page stays, with
// why.
func (s *Server) pollTenantHealth(ctx context.Context, used map[string]provision.SaaSService) {
	wanted := false
	for id := range used {
		if _, ok := tenantServices[id]; ok {
			wanted = true
		}
	}
	if !wanted || !s.mgr.M365().Configured() {
		return
	}
	issues, err := s.readTenantHealth(ctx)
	now := time.Now().Unix()
	vendors.mu.Lock()
	defer vendors.mu.Unlock()
	for id, svc := range used {
		names, ok := tenantServices[id]
		if !ok {
			continue
		}
		if err != nil {
			s.logger.Debug("tenant service health: could not read", "err", err)
			if w, ok := vendors.words[id]; ok {
				w.Note = "Your tenant's service health could not be read (" + err.Error() + "): this is Microsoft's public status page."
				vendors.words[id] = w
			}
			continue
		}
		w := tenantWord(svc.Name, names, issues)
		w.Checked = now
		vendors.words[id] = w
	}
}

// tenantIncident reports whether an issue counts: an incident (an advisory is a heads-up, rarely
// felt), not yet resolved.
func tenantIncident(is mailread.ServiceIssue) bool {
	return !is.IsResolved && strings.EqualFold(is.Classification, "incident")
}

func countIncidents(issues []mailread.ServiceIssue) int {
	n := 0
	for _, is := range issues {
		if tenantIncident(is) {
			n++
		}
	}
	return n
}

// tenantWord is the tenant's service health for one catalog service: its services' open incidents,
// an interruption being major and the rest minor, the worst first, then the earliest.
func tenantWord(service string, names []string, issues []mailread.ServiceIssue) vendorWord {
	w := vendorWord{Service: service, Who: "Microsoft", Source: "tenant", Page: "https://admin.microsoft.com/#/servicehealth"}
	of := func(svc string) bool {
		if names == nil {
			return true
		}
		for _, n := range names {
			if strings.EqualFold(n, svc) {
				return true
			}
		}
		return false
	}
	impact := func(is mailread.ServiceIssue) string {
		if strings.EqualFold(is.Status, "serviceInterruption") {
			return "major"
		}
		return "minor"
	}
	var best *mailread.ServiceIssue
	var bestAt int64
	for i := range issues {
		is := issues[i]
		if !tenantIncident(is) || !of(is.Service) {
			continue
		}
		w.Open++
		var at int64
		if t, err := time.Parse(time.RFC3339, is.Start); err == nil {
			at = t.Unix()
		}
		if best == nil || impactRank[impact(is)] > impactRank[impact(*best)] ||
			(impact(is) == impact(*best) && at > 0 && (bestAt == 0 || at < bestAt)) {
			best, bestAt = &issues[i], at
		}
	}
	if best != nil {
		w.Impact, w.Since = impact(*best), bestAt
		w.Title = strings.TrimSpace(best.ID + " " + strings.TrimSpace(best.Title))
		if names == nil && best.Service != "" {
			w.Title += " (" + best.Service + ")"
		}
	}
	return w
}

// fetchVendorWord reads one vendor's open incidents.
func fetchVendorWord(ctx context.Context, c *http.Client, svc provision.SaaSService) (vendorWord, error) {
	return fetchVendorWordShared(ctx, c, svc, nil)
}

// fetchVendorWordShared is fetchVendorWord reading each source once into bodies (nil: no sharing).
func fetchVendorWordShared(ctx context.Context, c *http.Client, svc provision.SaaSService, bodies map[string][]byte) (vendorWord, error) {
	st := svc.Status
	w := vendorWord{Service: svc.Name, Page: st.Page, Who: st.Who}
	url := st.API
	if st.Kind == "statuspage" {
		url = strings.TrimRight(st.API, "/") + "/api/v2/incidents/unresolved.json"
	}
	body, ok := bodies[url]
	if !ok {
		var err error
		if body, err = vendorGet(ctx, c, url); err != nil {
			return w, err
		}
		if bodies != nil {
			bodies[url] = body
		}
	}
	switch st.Kind {
	case "statuspage":
		return w, statuspageWord(&w, body)
	case "indicator":
		return w, indicatorWord(&w, body)
	case "slack":
		return w, slackWord(&w, body)
	case "m365":
		return w, m365PostWord(&w, body, tenantServices[svc.ID])
	case "azure":
		return w, azureFeedWord(&w, body)
	}
	return w, fmt.Errorf("unknown status kind %q", st.Kind)
}

// m365Quiet is what the Microsoft 365 card reads with nothing open.
var m365Quiet = map[string]bool{"": true, "operational": true, "available": true, "service restored": true,
	"resolved": true, "false positive": true, "post-incident report published": true}

// m365PostWord reads the Microsoft 365 card of Microsoft's service health page: one post for the
// whole suite, with its status ("Operational", "Service degradation", "Service interruption", ...).
// An interruption is major, anything else open minor. For a service of the suite (names: Outlook's
// "Exchange Online"), the post's own list of the services it hits ("- Exchange Online: Users may be
// unable to ...") gives the word: that service's line, or nothing when the list leaves it out. A post
// without such a list counts for every service; names nil (Microsoft 365) takes the post's title.
func m365PostWord(w *vendorWord, body []byte, names []string) error {
	var d struct {
		Title    string `json:"Title"`
		Message  string `json:"Message"`
		Status   string `json:"Status"`
		Authored string `json:"AuthoredDateTime"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(d.Status))
	if m365Quiet[status] {
		return nil
	}
	title := strings.TrimSpace(d.Title)
	if names != nil {
		if lines := m365ServiceLines(d.Message); len(lines) > 0 {
			title = ""
			for _, n := range names {
				if l, ok := lines[strings.ToLower(n)]; ok {
					title = l
					break
				}
			}
			if title == "" {
				return nil // the post lists the services it hits, and not this one
			}
			w.Detail = title
			title = sentencesUpTo(title, vendorTitleMax)
		}
	}
	w.Open, w.Impact, w.Title = 1, "minor", title
	if strings.Contains(status, "interruption") {
		w.Impact = "major"
	}
	if w.Title == "" {
		w.Title = strings.TrimSpace(d.Status)
	}
	if t, err := time.Parse(time.RFC3339, d.Authored); err == nil && t.Year() > 2000 {
		w.Since = t.Unix()
	}
	return nil
}

var (
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>|</?(div|p|li|ul)[^>]*>`)
	htmlTag   = regexp.MustCompile(`<[^>]+>`)
)

// m365ServiceLines reads the services a Microsoft 365 post lists ("- Exchange Online: Users may be
// unable to access their mailbox ...") by lower-case name, each with what it says.
func m365ServiceLines(msg string) map[string]string {
	text := html.UnescapeString(htmlTag.ReplaceAllString(htmlBreak.ReplaceAllString(msg, "\n"), ""))
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\u00a0", " "))
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "\u2022 ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "-\u2022 "))
		name, says, ok := strings.Cut(line, ":")
		if !ok || len(name) > 60 {
			continue
		}
		if says = strings.TrimSpace(says); says != "" {
			out[strings.ToLower(strings.TrimSpace(name))] = says
		}
	}
	return out
}

// vendorTitleMax is about how long a service's line from a Microsoft 365 post may run in the word;
// the whole line is its Detail.
const vendorTitleMax = 280

// sentencesUpTo keeps the whole sentences of s that fit in max characters (the first one cut short,
// with an ellipsis, when even it doesn't). A service's line can open with what still works ("Most of
// the core functionality ... are not impacted.") before what fails, so one sentence isn't enough.
func sentencesUpTo(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	out := ""
	rest := s
	for rest != "" {
		i := strings.Index(rest, ". ")
		next := rest
		if i >= 0 {
			next = rest[:i+1]
		}
		if len([]rune(out+next)) > max {
			break
		}
		out += next
		if i < 0 {
			rest = ""
		} else {
			rest = rest[i+1:]
		}
	}
	if out = strings.TrimSpace(out); out == "" {
		r := []rune(s)
		return strings.TrimSpace(string(r[:max-1])) + "\u2026"
	}
	return out
}

// azureFeedWord reads Azure's status RSS feed: each item is an incident open (its impact unsaid),
// the earliest named.
func azureFeedWord(w *vendorWord, body []byte) error {
	var d struct {
		Items []struct {
			Title   string `xml:"title"`
			PubDate string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &d); err != nil {
		return err
	}
	w.Open = len(d.Items)
	for _, it := range d.Items {
		var at int64
		for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 02 Jan 2006 15:04:05 Z"} {
			if t, err := time.Parse(layout, strings.TrimSpace(it.PubDate)); err == nil {
				at = t.Unix()
				break
			}
		}
		if w.Title == "" || (at > 0 && (w.Since == 0 || at < w.Since)) {
			w.Title, w.Since = strings.TrimSpace(it.Title), at
		}
	}
	return nil
}

func vendorGet(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Argus")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, vendorBodyMax))
}

// impactRank orders Statuspage's incident impacts.
var impactRank = map[string]int{"none": 0, "minor": 1, "major": 2, "critical": 3}

// statuspageWord reads /api/v2/incidents/unresolved.json: every incident not yet resolved (one in
// "monitoring" too: the vendor isn't sure yet), the worst impact first, the earlier one on a tie.
func statuspageWord(w *vendorWord, body []byte) error {
	var d struct {
		Incidents []struct {
			Name      string `json:"name"`
			Impact    string `json:"impact"`
			CreatedAt string `json:"created_at"`
			StartedAt string `json:"started_at"`
		} `json:"incidents"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return err
	}
	w.Open = len(d.Incidents)
	best := -1
	for i, in := range d.Incidents {
		if best < 0 || impactRank[in.Impact] > impactRank[d.Incidents[best].Impact] {
			best = i
		}
	}
	if best >= 0 {
		in := d.Incidents[best]
		w.Title, w.Impact = strings.TrimSpace(in.Name), in.Impact
		if _, ok := impactRank[w.Impact]; !ok {
			w.Impact = "none"
		}
		for _, ts := range []string{in.StartedAt, in.CreatedAt} {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				w.Since = t.Unix()
				break
			}
		}
	}
	return nil
}

// indicatorWord reads a page that gives only its indicator (/api/v2/status.json): anything but
// none or maintenance is one incident, named by the page's description.
func indicatorWord(w *vendorWord, body []byte) error {
	var d struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return err
	}
	if _, ok := impactRank[d.Status.Indicator]; ok && d.Status.Indicator != "none" {
		w.Open, w.Impact, w.Title = 1, d.Status.Indicator, strings.TrimSpace(d.Status.Description)
	}
	return nil
}

// slackWord reads Slack's own API: its active incidents, an outage being major, an incident minor
// and a notice without impact.
func slackWord(w *vendorWord, body []byte) error {
	var d struct {
		Status    string `json:"status"`
		Incidents []struct {
			Title       string `json:"title"`
			Type        string `json:"type"`
			DateCreated string `json:"date_created"`
		} `json:"active_incidents"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return err
	}
	rank := map[string]string{"outage": "major", "incident": "minor", "notice": "none"}
	w.Open = len(d.Incidents)
	best := -1
	for i, in := range d.Incidents {
		if best < 0 || impactRank[rank[in.Type]] > impactRank[rank[d.Incidents[best].Type]] {
			best = i
		}
	}
	if best >= 0 {
		in := d.Incidents[best]
		w.Title, w.Impact = strings.TrimSpace(in.Title), rank[in.Type]
		if w.Impact == "" {
			w.Impact = "none"
		}
		if t, err := time.Parse(time.RFC3339, in.DateCreated); err == nil {
			w.Since = t.Unix()
		}
	}
	return nil
}

// vendorLine is the vendor's word as an alert's line: "GitHub's status page: no incident open", or
// `Zoom's status page: minor incident "Degraded meeting joins" since 14:05 (and 1 more)`.
func vendorLine(w *vendorWord, loc *time.Location) string {
	if w == nil {
		return ""
	}
	who := w.Service + "'s status page: "
	switch {
	case w.Source == "tenant":
		who = "Microsoft 365 service health: "
	case w.Who != "":
		who = w.Who + "'s status page: "
	}
	if w.Open == 0 {
		return who + "no incident open"
	}
	kind := "incident"
	if w.Impact != "" && w.Impact != "none" {
		kind = w.Impact + " incident"
	}
	out := who + kind
	if w.Title != "" {
		out += ` "` + w.Title + `"`
	}
	if w.Since > 0 {
		t := time.Unix(w.Since, 0).In(loc)
		if time.Since(t) > 20*time.Hour {
			out += " since " + t.Format("Jan 2 15:04")
		} else {
			out += " since " + t.Format("15:04")
		}
	}
	if w.Open > 1 {
		out += fmt.Sprintf(" (and %d more)", w.Open-1)
	}
	return out
}
