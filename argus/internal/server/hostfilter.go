// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"strings"
)

// hostFilter narrows a list to the hosts one of some probes monitors and in one of some groups, the
// probe and group filters of the Changes, History and Inventory lists. A group covers its subgroups.
type hostFilter struct {
	probes []string // proxy ids; "0" = the server
	groups []string // group names
}

// parseHostFilter reads ?probe=10&probe=0&group=site1&group=site2/Network (a group name may hold a
// comma, so each value is one name; probe ids may also come comma-separated).
func parseHostFilter(r *http.Request) hostFilter {
	q := r.URL.Query()
	var f hostFilter
	for _, v := range q["probe"] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				f.probes = append(f.probes, p)
			}
		}
	}
	for _, g := range q["group"] {
		if g = strings.TrimSpace(g); g != "" {
			f.groups = append(f.groups, g)
		}
	}
	return f
}

func (f hostFilter) active() bool { return len(f.probes) > 0 || len(f.groups) > 0 }

// matches says whether a host passes the filter.
func (f hostFilter) matches(h hostInfo) bool {
	if len(f.probes) > 0 {
		ok := false
		for _, p := range f.probes {
			if p == h.ProxyID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.groups) > 0 {
		ok := false
		for _, want := range f.groups {
			for _, g := range h.Groups {
				if siteCovers(want, g) {
					ok = true
					break
				}
			}
			if ok {
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// filterHostIDs is the set of hosts that pass f; nil when f filters nothing.
func (s *Server) filterHostIDs(ctx context.Context, f hostFilter) (map[string]bool, error) {
	if !f.active() {
		return nil, nil
	}
	idx, err := s.hostIndex(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, h := range idx {
		if f.matches(h) {
			out[id] = true
		}
	}
	return out, nil
}
