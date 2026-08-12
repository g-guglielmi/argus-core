package server

import (
	"context"
	"net/http"
	"time"
)

type problemRow struct {
	EventID      string   `json:"event_id"`
	Name         string   `json:"name"`
	HostID       string   `json:"host_id"`
	HostName     string   `json:"host_name"`
	Severity     int      `json:"severity"`
	State        string   `json:"state"` // ok | warning | error
	Acknowledged bool     `json:"acknowledged"`
	AckUntil     *int64   `json:"ack_until,omitempty"`
	Clock        int64    `json:"clock"`
	ItemIDs      []string `json:"item_ids"` // sensors the trigger references (for deep-linking)
}

// handleProblems returns every active problem across all hosts, excluding those on hidden or
// paused hosts (and whose only sensors are hidden). The UI filters by severity/ack per view.
func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	problems, err := s.zbx.AllProblems(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}

	tids := make([]string, 0, len(problems))
	for _, p := range problems {
		tids = append(tids, p.ObjectID)
	}
	targets, _ := s.zbx.TriggerTargets(ctx, tids)
	hiddenHosts, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")
	hiddenItems, _ := s.st.ActiveSuppressionMap(ctx, "hide", "item")
	acked, _ := s.st.ActiveSuppressionMap(ctx, "ack", "event")

	out := make([]problemRow, 0, len(problems))
	for _, p := range problems {
		t, ok := targets[p.ObjectID]
		if !ok || len(t.Hosts) == 0 {
			continue // can't attribute to a host; skip
		}
		h := t.Hosts[0]
		if _, hostHidden := hiddenHosts[h.HostID]; hostHidden || h.Status == "1" { // hidden or paused
			continue
		}
		// Skip if the trigger's sensors are all hidden in Argus.
		if len(t.Items) > 0 {
			allHidden := true
			for _, it := range t.Items {
				if _, ok := hiddenItems[it.ItemID]; !ok {
					allHidden = false
					break
				}
			}
			if allHidden {
				continue
			}
		}
		sev := atoi(p.Severity)
		itemIDs := make([]string, 0, len(t.Items))
		for _, it := range t.Items {
			itemIDs = append(itemIDs, it.ItemID)
		}
		row := problemRow{
			EventID:  p.EventID,
			Name:     p.Name,
			HostID:   h.HostID,
			HostName: h.Name,
			Severity: sev,
			State:    severityState(sev),
			Clock:    atoi64(p.Clock),
			ItemIDs:  itemIDs,
		}
		if u, ok := acked[p.EventID]; ok {
			row.Acknowledged = true
			row.AckUntil = u
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}
