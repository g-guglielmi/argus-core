// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

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
	for _, id := range ids {
		svc := used[id]
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		w, err := fetchVendorWord(fctx, vendorClient, svc)
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
}

// fetchVendorWord reads one vendor's open incidents.
func fetchVendorWord(ctx context.Context, c *http.Client, svc provision.SaaSService) (vendorWord, error) {
	st := svc.Status
	w := vendorWord{Service: svc.Name, Page: st.Page}
	url := st.API
	if st.Kind == "statuspage" {
		url = strings.TrimRight(st.API, "/") + "/api/v2/incidents/unresolved.json"
	}
	body, err := vendorGet(ctx, c, url)
	if err != nil {
		return w, err
	}
	switch st.Kind {
	case "statuspage":
		return w, statuspageWord(&w, body)
	case "indicator":
		return w, indicatorWord(&w, body)
	case "slack":
		return w, slackWord(&w, body)
	}
	return w, fmt.Errorf("unknown status kind %q", st.Kind)
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
