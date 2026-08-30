package server

import (
	"context"
	"net/http"
	"time"
)

type triggerHost struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// triggerRow is one alert rule for the Triggers tabs: its name, severity, whether it's firing, since
// when, the host(s) it lives on and the sensor(s) its expression watches (>1 = a multi-sensor trigger).
type triggerRow struct {
	ID          string        `json:"id"`
	Description string        `json:"description"`
	Severity    int           `json:"severity"` // 0..5
	Enabled     bool          `json:"enabled"`
	Problem     bool          `json:"problem"` // currently firing
	Since       int64         `json:"since"`   // unix time it entered its current state
	Hosts       []triggerHost `json:"hosts"`
	Sensors     []string      `json:"sensors"`
}

// handleTriggers lists the monitored triggers (alert rules) across all hosts - the data behind both
// Triggers tabs (the "firing" flat list filters problem=true; "all" groups by host). Triggers whose
// hosts are all hidden in Argus are omitted, to match the rest of the UI.
func (s *Server) handleTriggers(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	trs, err := s.zbx.AllTriggers(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	hideHost, _ := s.st.ActiveSuppressionMap(ctx, "hide", "host")

	out := make([]triggerRow, 0, len(trs))
	for _, t := range trs {
		hosts := make([]triggerHost, 0, len(t.Hosts))
		allHidden := len(t.Hosts) > 0
		for _, h := range t.Hosts {
			if h.Status == "3" { // template host - skip (shouldn't appear with monitored:true, but be safe)
				continue
			}
			if _, hidden := hideHost[h.HostID]; !hidden {
				allHidden = false
			}
			hosts = append(hosts, triggerHost{ID: h.HostID, Name: h.Name})
		}
		if len(hosts) == 0 || allHidden {
			continue
		}
		sensors := make([]string, 0, len(t.Items))
		for _, it := range t.Items {
			sensors = append(sensors, it.Name)
		}
		out = append(out, triggerRow{
			ID: t.TriggerID, Description: t.Description, Severity: atoi(t.Priority),
			Enabled: t.Status == "0", Problem: t.Value == "1", Since: atoi64(t.LastChange),
			Hosts: hosts, Sensors: sensors,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
