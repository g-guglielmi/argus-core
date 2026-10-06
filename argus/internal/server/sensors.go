// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"net/http"
	"sort"

	"argus/internal/zabbix"
)

type sensorRow struct {
	key       string   // Zabbix item key (server-side only: formats 1/0 reachability readings)
	HostID    string   `json:"host_id"`
	HostName  string   `json:"host_name"`
	ItemID    string   `json:"item_id"`
	Name      string   `json:"name"`
	Label     string   `json:"label,omitempty"`
	Category  string   `json:"category,omitempty"`
	Value     string   `json:"value"`
	Units     string   `json:"units"`
	LastClock int64    `json:"last_clock"`
	State     string   `json:"state"` // ok | warning | error | acked | paused | hidden
	Numeric   bool     `json:"numeric"`
	Supported bool     `json:"supported"`
	Priority  int      `json:"priority"`         // PRTG-style display priority 1..5 (Argus-only)
	Severity  int      `json:"severity"`         // worst Zabbix trigger severity 0..5 (0 = none)
	Reason    string   `json:"reason,omitempty"` // name of the worst trigger, i.e. why the sensor is unhappy
	Why       string   `json:"why,omitempty"`    // why it isn't reading: Zabbix's error, or its collector's reason (reasons.go)
	Since     int64    `json:"since,omitempty"`  // unix time the worst problem started firing (for its age)
	EventIDs  []string `json:"event_ids"`        // problem events on this sensor (for ack / unack from a list)
	// Synthetic marks a row that isn't a Zabbix sensor but an Argus-raised problem with none (an agent
	// or SNMP endpoint that stopped answering): it has no chart and can't be paused or hidden itself.
	Synthetic bool `json:"synthetic,omitempty"`
	// Maintenance is the window its host is in right now (alerts held); set per response, never on
	// the shared census rows.
	Maintenance *maintHit `json:"maintenance,omitempty"`
	// HeldBy is the master whose outage holds this sensor's alerts (master.go); the lists fold it under
	// that master. Holds is, on a master's row, how many sensors it holds.
	HeldBy *heldRef `json:"held_by,omitempty"`
	Holds  int      `json:"holds,omitempty"`
	// Note is the note left on the sensor while it is in trouble (sensornotes.go).
	Note *sensorNoteView `json:"note,omitempty"`
	// Call is who to call when the sensor measures one of its site's internet lines (siteinfo.go).
	Call string `json:"call,omitempty"`
}

// interfaceRowLabel names the census row of an unreachable interface after what stopped answering.
func interfaceRowLabel(problem string) string {
	switch problem {
	case "Zabbix agent not reachable":
		return "Zabbix agent"
	case "SNMP not responding":
		return "SNMP"
	case "IPMI not reachable":
		return "IPMI"
	case "JMX not reachable":
		return "JMX"
	}
	return "Monitoring interface"
}

// handleSensors returns a census of the curated ("key") sensors across every host, each tagged
// with a single state, so the UI can show status-summary counts and per-state filtered lists.
// State precedence: hidden > paused > error > warning > acknowledged > ok. Unsupported sensors
// that are otherwise ok are skipped (they're "unknown", not ok); one that stopped collecting has an
// Argus-raised problem, so it counts as an error. A sensor outside the curated list is included only
// while it has a problem.
func (s *Server) handleSensors(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), censusBuildTime)
	defer cancel()
	out, err := s.sensorCensus(ctx)
	if err == nil {
		out, err = s.scopedRows(ctx, scopeFrom(r), out)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// scopedRows keeps the census rows of the hosts the scope sees (a new slice; the census's own rows
// are shared and never modified).
func (s *Server) scopedRows(ctx context.Context, sc siteScope, rows []sensorRow) ([]sensorRow, error) {
	if sc.all {
		return rows, nil
	}
	vis, err := s.visibleHosts(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make([]sensorRow, 0, len(rows))
	for _, r := range rows {
		if vis[r.HostID] {
			out = append(out, r)
		}
	}
	return out, nil
}

// sensorCensus is every curated sensor across all hosts with its single state (see handleSensors),
// sorted by host then name, from the census kept in memory (census.go). Shared by the status pills,
// the Overview and the status pages. The rows are shared: callers must not modify them.
func (s *Server) sensorCensus(ctx context.Context) ([]sensorRow, error) {
	if s.census == nil { // a Server assembled by hand (tests) has no cache
		return s.buildCensus(ctx)
	}
	snap, err := s.census.get(ctx)
	return snap.Rows, err
}

// buildCensus reads the census from Zabbix and the Argus store.
func (s *Server) buildCensus(ctx context.Context) ([]sensorRow, error) {
	items, err := s.zbx.AllItems(ctx)
	if err != nil {
		return nil, err
	}

	// Per-item problem state: worst unacknowledged severity, and whether it has an acked problem.
	problems, _ := s.zbx.AllProblems(ctx)
	tids := make([]string, 0, len(problems))
	for _, p := range problems {
		tids = append(tids, p.ObjectID)
	}
	// The triggers' sensors (and expressions and hosts, which the master holds need).
	targets, _ := s.zbx.TriggerTargets(ctx, tids)
	if targets == nil {
		targets = map[string]zabbix.TriggerTarget{}
	}
	itemsByTrigger := make(map[string][]string, len(targets))
	for tid, t := range targets {
		for _, it := range t.Items {
			itemsByTrigger[tid] = append(itemsByTrigger[tid], it.ItemID)
		}
	}
	// Argus-raised problems (a sensor that stopped collecting) put their sensor in error like any other.
	synth := syntheticProblems(ctx, s.st, s.zbx, false)
	for _, p := range synth.problems {
		for _, it := range synth.targets[p.ObjectID].Items {
			itemsByTrigger[p.ObjectID] = append(itemsByTrigger[p.ObjectID], it.ItemID)
		}
		targets[p.ObjectID] = synth.targets[p.ObjectID]
		problems = append(problems, p)
	}
	acked, _ := s.st.ActiveSuppressionMap(ctx, "ack", "event")
	unackedRank := map[string]int{} // 1 = warning, 2 = error
	hasAcked := map[string]bool{}
	itemEvents := map[string][]string{}
	itemSev := map[string]int{}       // worst Zabbix trigger severity (0..5) firing on the item
	itemReason := map[string]string{} // name of that worst trigger, so the sensor can show *why* it's unhappy
	itemSince := map[string]int64{}   // when that worst problem started firing (unix), for the "how long" age
	for _, p := range problems {
		rank := 0
		switch severityState(atoi(p.Severity)) {
		case "error":
			rank = 2
		case "warning":
			rank = 1
		default:
			continue
		}
		sev := atoi(p.Severity)
		_, isAcked := acked[p.EventID]
		for _, itemID := range itemsByTrigger[p.ObjectID] {
			itemEvents[itemID] = append(itemEvents[itemID], p.EventID)
			if isAcked {
				hasAcked[itemID] = true
			} else if rank > unackedRank[itemID] {
				unackedRank[itemID] = rank
			}
			if sev > itemSev[itemID] { // track the highest-severity trigger + its name/start for this item
				itemSev[itemID] = sev
				itemReason[itemID] = p.Name
				itemSince[itemID] = atoi64(p.Clock)
			}
		}
	}

	hideItem, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	hideHost, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	prioMap, _ := s.st.ItemPriorities(ctx)

	reasons := reasonIndex{}
	for _, it := range items {
		if len(it.Hosts) > 0 {
			reasons.add(it.Hosts[0].HostID, it.Key, it.LastValue)
		}
	}

	out := make([]sensorRow, 0, len(items))
	for _, it := range items {
		if len(it.Hosts) == 0 {
			continue
		}
		host := it.Hosts[0]
		if host.Status != "0" && host.Status != "1" { // skip template items
			continue
		}
		cat, label, _, _, ok := classifyItem(it.Key, it.Name)
		if !ok {
			// Curated key sensors only - except one with a problem: a flag the curated list leaves out
			// (a collector's "reachable"), or a sensor from a stock Zabbix template, still counts
			// while it is unhappy, under its Zabbix name.
			if unackedRank[it.ItemID] == 0 && !hasAcked[it.ItemID] {
				continue
			}
			label = it.Name
			if c, isColl := collectorOf(it.Key); isColl {
				cat, label = c.category, c.label
			}
		}
		_, hiddenItem := hideItem[it.ItemID]
		_, hiddenHost := hideHost[host.HostID]
		state := "ok"
		switch {
		case hiddenItem || hiddenHost:
			state = "hidden"
		case it.Status == "1" || host.Status == "1":
			state = "paused"
		case unackedRank[it.ItemID] == 2:
			state = "error"
		case unackedRank[it.ItemID] == 1:
			state = "warning"
		case hasAcked[it.ItemID]:
			state = "acked"
		}
		supported := it.State == "0"
		if state == "ok" && !supported {
			continue // unsupported & otherwise-ok = "unknown"; don't count as ok
		}
		// A 1/0 reachability (ping, a service check, a collector's "reachable") reads as words.
		value, units := it.LastValue, it.Units
		if r, ok := reachabilityReading(it.Key, it.LastValue); ok {
			value, units = r, ""
		}
		// An empty list, never null: the lists read its length on every row, OK ones included.
		events := itemEvents[it.ItemID]
		if events == nil {
			events = []string{}
		}
		out = append(out, sensorRow{
			key: it.Key, HostID: host.HostID, HostName: host.Name, ItemID: it.ItemID, Name: it.Name,
			Label: label, Category: cat, Value: value, Units: units, LastClock: atoi64(it.LastClock),
			State: state, Numeric: numericValueType(it.ValueType), Supported: supported,
			Priority: priorityOf(prioMap, it.ItemID), Severity: itemSev[it.ItemID], Reason: itemReason[it.ItemID],
			Since: itemSince[it.ItemID], EventIDs: events,
			Why: sensorWhy(reasons, host.HostID, it.Key, it.LastValue, it.Error, supported),
		})
	}
	// Problems that belong to no sensor (Argus-raised: an agent or SNMP endpoint that stopped
	// answering) get a row of their own, so the pills, the Overview and the status pages count them.
	for _, p := range synth.problems {
		t := synth.targets[p.ObjectID]
		if len(t.Items) > 0 || len(t.Hosts) == 0 {
			continue
		}
		h := t.Hosts[0]
		if _, hidden := hideHost[h.HostID]; hidden || h.Status == "1" {
			continue
		}
		state := severityState(atoi(p.Severity))
		if _, isAcked := acked[p.EventID]; isAcked {
			state = "acked"
		}
		out = append(out, sensorRow{
			HostID: h.HostID, HostName: h.Name, ItemID: p.EventID, Name: interfaceRowLabel(p.Name), Label: interfaceRowLabel(p.Name),
			Category: "Availability", Value: "Not reachable", State: state, Supported: true, Priority: defaultItemPriority,
			Severity: atoi(p.Severity), Reason: p.Name, Since: atoi64(p.Clock), LastClock: atoi64(p.Clock), EventIDs: []string{p.EventID},
			Synthetic: true,
		})
	}
	s.markHeld(ctx, out, problems, targets)
	s.markCalls(ctx, out)
	if notes, err := s.st.LiveSensorNotes(ctx); err == nil && len(notes) > 0 {
		for i := range out {
			if n, ok := notes[out[i].ItemID]; ok {
				out[i].Note = noteViewOf(n)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
