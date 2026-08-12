package server

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// handleSpark returns a compact recent series (down to ~24 values) per requested item, for the
// inline sparklines. One item.get (value types) + up to two history.get (float + unsigned).
// GET /api/spark?items=id1,id2,...&range=2h
func (s *Server) handleSpark(w http.ResponseWriter, r *http.Request) {
	if !s.zbx.Authenticated() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Zabbix API token not configured (set ARGUS_ZABBIX_API_TOKEN)"})
		return
	}
	idsParam := r.URL.Query().Get("items")
	if strings.TrimSpace(idsParam) == "" {
		writeJSON(w, http.StatusOK, map[string][]float64{})
		return
	}
	ids := strings.Split(idsParam, ",")
	if len(ids) > 300 { // safety cap
		ids = ids[:300]
	}
	rng, ok := timeRanges[r.URL.Query().Get("range")]
	if !ok {
		rng = timeRanges["2h"]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	types, err := s.zbx.ItemValueTypes(ctx, ids)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Zabbix: " + err.Error()})
		return
	}
	byType := map[int][]string{}
	for id, vt := range types {
		if vt == "0" || vt == "3" { // only numeric items have a sparkline
			byType[atoi(vt)] = append(byType[atoi(vt)], id)
		}
	}
	from := time.Now().Unix() - int64(rng.dur.Seconds())
	series := map[string][]float64{}
	for vt, group := range byType {
		pts, err := s.zbx.HistoryMulti(ctx, group, vt, from)
		if err != nil {
			continue
		}
		for _, p := range pts {
			if v := pf(p.Value); v != nil {
				series[p.ItemID] = append(series[p.ItemID], *v)
			}
		}
	}
	out := make(map[string][]float64, len(series))
	for id, vals := range series {
		out[id] = downsample(vals, 24)
	}
	writeJSON(w, http.StatusOK, out)
}

// downsample reduces a series to at most n points, keeping the first and last.
func downsample(vals []float64, n int) []float64 {
	if len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = vals[i*(len(vals)-1)/(n-1)]
	}
	return out
}
