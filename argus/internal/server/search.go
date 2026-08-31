package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// searchResult is one hit in the global quick-switcher. Type is "host", "sensor" or "group"; the
// id fields tell the SPA where to navigate (host -> tree host, sensor -> its chart, group -> focus).
type searchResult struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	Sub    string `json:"sub"`
	HostID string `json:"host_id,omitempty"`
	ItemID string `json:"item_id,omitempty"`
	Group  string `json:"group,omitempty"`
}

const searchPerType = 6 // cap on hosts / sensors / groups each, keeping the switcher short

// handleSearch powers the top-bar quick-switcher: a case-insensitive substring match over host
// names + IPs, sensor names, and host-group names. Argus-side (a single Zabbix sweep per query) -
// fine at homelab scale; move behind an index if the census grows large.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix is not configured"})
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeJSON(w, http.StatusOK, []searchResult{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	hosts, _ := s.zbx.Hosts(ctx)
	ips, _ := s.zbx.HostIPs(ctx)
	items, _ := s.zbx.AllItems(ctx)
	hiddenPaths, _ := s.st.HiddenGroups(ctx)
	hidden := make(map[string]bool, len(hiddenPaths))
	for _, p := range hiddenPaths {
		hidden[p] = true
	}

	// Hosts: match name or IP. Carry the match rank so prefix hits sort above mid-string ones.
	type ranked struct {
		res  searchResult
		rank int
	}
	var hostHits []ranked
	for _, h := range hosts {
		ip := ips[h.HostID]
		rk := matchRank(strings.ToLower(h.Name), q)
		if rk < 0 && ip != "" && strings.Contains(strings.ToLower(ip), q) {
			rk = 2 // IP match ranks just below a name match
		}
		if rk < 0 {
			continue
		}
		sub := "Host"
		if ip != "" {
			sub = ip
		}
		hostHits = append(hostHits, ranked{searchResult{Type: "host", Label: h.Name, Sub: sub, HostID: h.HostID}, rk})
	}

	// Groups: distinct host-group names (excluding tree-hidden ones, which have no tree focus).
	seenGroup := map[string]bool{}
	var groupHits []ranked
	for _, h := range hosts {
		for _, g := range h.Groups {
			if seenGroup[g.Name] || hidden[g.Name] {
				continue
			}
			rk := matchRank(strings.ToLower(g.Name), q)
			if rk < 0 {
				continue
			}
			seenGroup[g.Name] = true
			groupHits = append(groupHits, ranked{searchResult{Type: "group", Label: g.Name, Sub: "Group", Group: g.Name}, rk})
		}
	}

	// Sensors: match the item name; skip template items (host status 3).
	var sensorHits []ranked
	for _, it := range items {
		rk := matchRank(strings.ToLower(it.Name), q)
		if rk < 0 {
			continue
		}
		hostID, hostName := "", ""
		if len(it.Hosts) > 0 {
			if it.Hosts[0].Status == "3" {
				continue
			}
			hostID, hostName = it.Hosts[0].HostID, it.Hosts[0].Name
		}
		sensorHits = append(sensorHits, ranked{searchResult{Type: "sensor", Label: it.Name, Sub: hostName, HostID: hostID, ItemID: it.ItemID}, rk})
	}

	trim := func(hits []ranked) []searchResult {
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].rank != hits[j].rank {
				return hits[i].rank < hits[j].rank
			}
			return hits[i].res.Label < hits[j].res.Label
		})
		if len(hits) > searchPerType {
			hits = hits[:searchPerType]
		}
		out := make([]searchResult, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.res)
		}
		return out
	}

	// Order in the response: hosts, then sensors, then groups.
	out := trim(hostHits)
	out = append(out, trim(sensorHits)...)
	out = append(out, trim(groupHits)...)
	writeJSON(w, http.StatusOK, out)
}

// matchRank scores how well q matches s: 0 = exact, 1 = prefix, 2 = word-boundary, 3 = substring,
// -1 = no match. Lower is better; used to sort stronger matches first.
func matchRank(s, q string) int {
	if s == q {
		return 0
	}
	if strings.HasPrefix(s, q) {
		return 1
	}
	i := strings.Index(s, q)
	if i < 0 {
		return -1
	}
	if s[i-1] == ' ' || s[i-1] == '/' || s[i-1] == '-' || s[i-1] == '_' {
		return 2
	}
	return 3
}
