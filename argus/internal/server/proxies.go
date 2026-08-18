package server

import (
	"context"
	"net/http"
	"time"
)

type proxyView struct {
	Name         string `json:"name"`
	LastAccess   int64  `json:"last_access"`   // unix seconds Zabbix last heard data, 0 if never seen
	Online       bool   `json:"online"`        // seen within the last 5 minutes
	Mode         string `json:"mode"`          // active | passive
	EnrolledAt   int64  `json:"enrolled_at"`   // unix seconds a probe self-enrolled via Argus; 0 if manual
	Version      string `json:"version"`       // running probe image version reported at check-in ("" = unknown)
	Target       string `json:"target"`        // fleet target version this probe should converge on
	SelfUpdate   bool   `json:"selfupdate"`    // probe reports its self-updater is enabled
	UpdateStatus string `json:"update_status"` // unknown | tracking | current | outdated
	LastCheckin  int64  `json:"last_checkin"`  // unix seconds of the last Argus check-in (0 = never)
}

// handleProxies lists Zabbix proxies (the per-site collectors) with their last-access time, so
// the Probes view can show which sites are actually reporting instead of placeholder data.
func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	enrolled, err := s.st.EnrollmentTimes(ctx) // best-effort; a nil map still indexes safely below
	if err != nil {
		s.logger.Warn("proxies: enrollment times lookup failed", "err", err)
	}
	agents, err := s.st.ProbeAgents(ctx) // best-effort fleet-update state (version / self-update)
	if err != nil {
		s.logger.Warn("proxies: probe agents lookup failed", "err", err)
	}
	target, err := s.st.ProbeTargetVersion(ctx)
	if err != nil {
		s.logger.Warn("proxies: probe target lookup failed", "err", err)
		target = "latest"
	}
	now := time.Now().Unix()
	out := make([]proxyView, 0, len(proxies))
	for _, p := range proxies {
		la := atoi64(p.LastAccess)
		mode := "active"
		if p.Mode == "1" {
			mode = "passive"
		}
		ag := agents[p.Name]
		out = append(out, proxyView{
			Name:         p.Name,
			LastAccess:   la,
			Online:       la > 0 && now-la <= 300,
			Mode:         mode,
			EnrolledAt:   enrolled[p.Name],
			Version:      ag.Version,
			Target:       target,
			SelfUpdate:   ag.SelfUpdate,
			UpdateStatus: updateStatus(ag.Version, target),
			LastCheckin:  ag.LastCheckin,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
