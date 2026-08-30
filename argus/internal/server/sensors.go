package server

import (
	"context"
	"net/http"
	"sort"
	"time"
)

type sensorRow struct {
	HostID    string `json:"host_id"`
	HostName  string `json:"host_name"`
	ItemID    string `json:"item_id"`
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Category  string `json:"category,omitempty"`
	Value     string `json:"value"`
	Units     string `json:"units"`
	LastClock int64  `json:"last_clock"`
	State     string   `json:"state"` // ok | warning | error | acked | paused | hidden
	Numeric   bool     `json:"numeric"`
	Supported bool     `json:"supported"`
	Priority  int      `json:"priority"`         // PRTG-style display priority 1..5 (Argus-only)
	Severity  int      `json:"severity"`         // worst Zabbix trigger severity 0..5 (0 = none)
	Reason    string   `json:"reason,omitempty"` // name of the worst trigger, i.e. why the sensor is unhappy
	EventIDs  []string `json:"event_ids"`        // problem events on this sensor (for ack / unack from a list)
}

// handleSensors returns a census of the curated ("key") sensors across every host, each tagged
// with a single state, so the UI can show status-summary counts and per-state filtered lists.
// State precedence: hidden > paused > error > warning > acknowledged > ok. Unsupported sensors
// that are otherwise ok are skipped (they're "unknown", not ok).
func (s *Server) handleSensors(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	items, err := s.zbx.AllItems(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}

	// Per-item problem state: worst unacknowledged severity, and whether it has an acked problem.
	problems, _ := s.zbx.AllProblems(ctx)
	tids := make([]string, 0, len(problems))
	for _, p := range problems {
		tids = append(tids, p.ObjectID)
	}
	itemsByTrigger, _ := s.zbx.TriggerItems(ctx, tids)
	acked, _ := s.st.ActiveSuppressionMap(ctx, "ack", "event")
	unackedRank := map[string]int{} // 1 = warning, 2 = error
	hasAcked := map[string]bool{}
	itemEvents := map[string][]string{}
	itemSev := map[string]int{}       // worst Zabbix trigger severity (0..5) firing on the item
	itemReason := map[string]string{} // name of that worst trigger, so the sensor can show *why* it's unhappy
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
			if sev > itemSev[itemID] { // track the highest-severity trigger + its name for this item
				itemSev[itemID] = sev
				itemReason[itemID] = p.Name
			}
		}
	}

	hideItem, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	hideHost, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	prioMap, _ := s.st.ItemPriorities(ctx)

	out := make([]sensorRow, 0, len(items))
	for _, it := range items {
		if len(it.Hosts) == 0 {
			continue
		}
		host := it.Hosts[0]
		if host.Status != "0" && host.Status != "1" { // skip template items
			continue
		}
		cat, label, ok := classifyItem(it.Key, it.Name)
		if !ok { // curated key sensors only
			continue
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
		out = append(out, sensorRow{
			HostID: host.HostID, HostName: host.Name, ItemID: it.ItemID, Name: it.Name,
			Label: label, Category: cat, Value: it.LastValue, Units: it.Units, LastClock: atoi64(it.LastClock),
			State: state, Numeric: numericValueType(it.ValueType), Supported: supported,
			Priority: priorityOf(prioMap, it.ItemID), Severity: itemSev[it.ItemID], Reason: itemReason[it.ItemID],
			EventIDs: itemEvents[it.ItemID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}
