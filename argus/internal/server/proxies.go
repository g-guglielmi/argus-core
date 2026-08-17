package server

import (
	"context"
	"net/http"
	"time"
)

type proxyView struct {
	Name       string `json:"name"`
	LastAccess int64  `json:"last_access"`  // unix seconds, 0 if never seen
	Online     bool   `json:"online"`       // seen within the last 5 minutes
	Mode       string `json:"mode"`         // active | passive
	EnrolledAt int64  `json:"enrolled_at"`  // unix seconds a probe self-enrolled via Argus; 0 if manual
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
	now := time.Now().Unix()
	out := make([]proxyView, 0, len(proxies))
	for _, p := range proxies {
		la := atoi64(p.LastAccess)
		mode := "active"
		if p.Mode == "1" {
			mode = "passive"
		}
		out = append(out, proxyView{
			Name:       p.Name,
			LastAccess: la,
			Online:     la > 0 && now-la <= 300,
			Mode:       mode,
			EnrolledAt: enrolled[p.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}
