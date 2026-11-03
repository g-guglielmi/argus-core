// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"argus/internal/auth"
	"argus/internal/diag"
	"argus/internal/store"
)

// Diagnostics (DESIGN 7q): a ping, a traceroute or a DNS lookup an admin or helpdesk user runs from
// the core or from a probe. The core runs its own in process (internal/diag); a probe's rides the
// check-in channel like a scan: the next check-in hands it out, the probe's argus_diag.py posts the
// results back as they come (POST /api/probes/diag-results), and the dialog polls the run.
//
// Runs live in memory only: they last minutes, and every one is in the change log. A probe checks in
// once a minute; while a run waits for it, or a dialog that may start one is open (warm), its
// check-in answer asks it back in diagFastCheckin instead.

const (
	diagFastCheckin = 3                // seconds between a warm probe's check-ins
	diagWarmFor     = 2 * time.Minute  // how long an open dialog keeps its probe warm
	diagPickupWait  = 90 * time.Second // a queued run the probe never took fails after this
	diagSilentAfter = 60 * time.Second // a probe run with no word back for this long fails
	diagRunLimit    = 2 * time.Minute  // the core's own run is cut off after this
	diagKeep        = 15 * time.Minute // a finished run can be read back this long
)

// diagRun is one diagnostic. Source is "core" or the probe's proxy name.
type diagRun struct {
	ID         int64
	Source     string
	SourceName string // "Core server" or the probe's name, for the dialog and the log
	Req        diag.Request
	State      string // queued | sent (a probe took it, no result yet) | running | done | failed
	Result     diag.Result
	By         int64
	Created    time.Time
	Updated    time.Time
}

// diagBoard holds the runs and which probes are warm.
type diagBoard struct {
	mu   sync.Mutex
	runs map[int64]*diagRun
	warm map[string]time.Time // proxy name -> until when its check-ins come fast
}

func newDiagBoard() *diagBoard {
	return &diagBoard{runs: map[int64]*diagRun{}, warm: map[string]time.Time{}}
}

// tidy fails the runs a probe never took or stopped reporting on, and forgets the old ones.
// The caller holds mu.
func (b *diagBoard) tidy(now time.Time) {
	for id, r := range b.runs {
		switch {
		case r.State == "queued" && now.Sub(r.Created) > diagPickupWait:
			r.State, r.Updated = "failed", now
			r.Result.Done = true
			r.Result.Error = r.SourceName + " did not pick it up: is it checking in to Argus?"
		case r.State == "sent" && now.Sub(r.Updated) > diagSilentAfter:
			r.State, r.Updated = "failed", now
			r.Result.Done = true
			r.Result.Error = r.SourceName + " took the run, but none of its results reached Argus: the probe's log says why"
		case r.State == "running" && r.Source != "core" && now.Sub(r.Updated) > diagSilentAfter:
			r.State, r.Updated = "failed", now
			r.Result.Done = true
			r.Result.Error = r.SourceName + " stopped reporting on it"
		case (r.State == "done" || r.State == "failed") && now.Sub(r.Updated) > diagKeep:
			delete(b.runs, id)
		}
	}
	for name, until := range b.warm {
		if now.After(until) {
			delete(b.warm, name)
		}
	}
}

// add queues a run unless one is still going from the same source.
func (b *diagBoard) add(r *diagRun) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tidy(r.Created)
	for _, o := range b.runs {
		if o.Source == r.Source && (o.State == "queued" || o.State == "sent" || o.State == "running") {
			return false
		}
	}
	for {
		var id [8]byte
		_, _ = rand.Read(id[:])
		// Positive and below 2^53, so the id survives the browser's numbers; a guess lands nowhere.
		r.ID = int64(binary.BigEndian.Uint64(id[:]) >> 11)
		if _, taken := b.runs[r.ID]; !taken && r.ID != 0 {
			break
		}
	}
	b.runs[r.ID] = r
	return true
}

// get is a copy of a run, tidied first.
func (b *diagBoard) get(id int64) (diagRun, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tidy(time.Now())
	r, ok := b.runs[id]
	if !ok {
		return diagRun{}, false
	}
	return *r, true
}

// diagJob is a run as a probe's check-in hands it out.
type diagJob struct {
	ID int64 `json:"id"`
	diag.Request
}

// take hands a probe its queued run (marking it running), if it has one.
func (b *diagBoard) take(proxyName string) *diagJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tidy(now)
	for _, r := range b.runs {
		if r.Source == proxyName && r.State == "queued" {
			r.State, r.Updated = "sent", now // running once its first result arrives
			return &diagJob{ID: r.ID, Request: r.Req}
		}
	}
	return nil
}

// fast reports whether a probe's next check-in should come quickly: a run waits for it, or a dialog
// that may start one is open.
func (b *diagBoard) fast(proxyName string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if until, ok := b.warm[proxyName]; ok && now.Before(until) {
		return true
	}
	for _, r := range b.runs {
		if r.Source == proxyName && r.State == "queued" {
			return true
		}
	}
	return false
}

func (b *diagBoard) warmUp(proxyName string) {
	b.mu.Lock()
	b.warm[proxyName] = time.Now().Add(diagWarmFor)
	b.mu.Unlock()
}

// report takes a result for a run: only the source's own, and only while it runs. "" when taken,
// else why not, for the log.
func (b *diagBoard) report(source string, id int64, res diag.Result) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[id]
	switch {
	case !ok:
		return "no such run (finished over 15 minutes ago, or Argus restarted since)"
	case r.Source != source:
		return "the run belongs to another source"
	case r.State != "sent" && r.State != "running":
		return "the run is already " + r.State
	}
	r.Result, r.Updated, r.State = res, time.Now(), "running"
	if res.Done {
		r.State = "done"
	}
	return ""
}

// diagSource is where a run can go: the core, or a probe that runs diagnostics and reports.
type diagSource struct {
	ID, Name, Proxy string // Proxy: the proxy name ("" for the core)
}

// resolveDiagSource is the source a request names ("core" or a proxy id), or why it can't run there.
func (s *Server) resolveDiagSource(ctx context.Context, r *http.Request, id string) (diagSource, int, string) {
	if id == "core" {
		return diagSource{ID: "core", Name: "Core server"}, 0, ""
	}
	if !s.proxyInScope(ctx, scopeFrom(r), id) {
		return diagSource{}, http.StatusNotFound, "that probe is not at one of your sites"
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return diagSource{}, http.StatusBadGateway, "could not reach Zabbix to find the probe"
	}
	for _, p := range proxies {
		if p.ProxyID != id {
			continue
		}
		ag, err := s.st.ProbeAgentByName(ctx, p.Name)
		switch {
		case err != nil:
			return diagSource{}, http.StatusConflict, p.Name + " does not report to Argus, so it can't run diagnostics"
		case !ag.Diags:
			return diagSource{}, http.StatusConflict, p.Name + " needs a probe update to run diagnostics"
		case time.Now().Unix()-ag.ReportAt > int64(reportStaleAfter/time.Second):
			return diagSource{}, http.StatusConflict, p.Name + " is not reporting to Argus right now"
		}
		return diagSource{ID: id, Name: p.Name, Proxy: p.Name}, 0, ""
	}
	return diagSource{}, http.StatusNotFound, "no such probe"
}

// diagView is a run as the dialog reads it.
type diagView struct {
	ID       int64       `json:"id"`
	Source   string      `json:"source"`
	Tool     string      `json:"tool"`
	Target   string      `json:"target"`
	State    string      `json:"state"`
	Created  int64       `json:"created"`
	Updated  int64       `json:"updated"`
	Result   diag.Result `json:"result"`
	Duration float64     `json:"duration"` // seconds from start to the last report
}

func viewDiag(r diagRun) diagView {
	return diagView{ID: r.ID, Source: r.SourceName, Tool: r.Req.Tool, Target: r.Req.Target, State: r.State,
		Created: r.Created.Unix(), Updated: r.Updated.Unix(), Result: r.Result,
		Duration: r.Updated.Sub(r.Created).Seconds()}
}

// diagToolName is a tool as the log names it.
var diagToolName = map[string]string{"ping": "Ping", "traceroute": "Traceroute", "dns": "DNS lookup"}

// POST /api/diag (admin, helpdesk): start a run. Body: source ("core" or a proxy id), tool, target,
// count, type, server, and optionally host_id (the host the dialog was opened on, for its own log).
func (s *Server) handleStartDiag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		diag.Request
		Source string `json:"source"`
		HostID string `json:"host_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	dreq, err := diag.Clean(req.Request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	key := "diag:" + strconv.FormatInt(u.ID, 10)
	if blocked, retry := s.diagLimit.Blocked(key); blocked {
		writeThrottled(w, retry)
		return
	}
	ctx, cancel := scopeLookupCtx(r)
	defer cancel()
	src, code, why := s.resolveDiagSource(ctx, r, strings.TrimSpace(req.Source))
	if why != "" {
		writeJSON(w, code, map[string]string{"error": why})
		return
	}
	hostIDs := []string{}
	if h := strings.TrimSpace(req.HostID); h != "" && s.hostInScope(ctx, scopeFrom(r), h) {
		hostIDs = append(hostIDs, h)
	}
	now := time.Now()
	run := &diagRun{Source: "core", SourceName: src.Name, Req: dreq, State: "queued", By: u.ID, Created: now, Updated: now}
	if src.Proxy != "" {
		run.Source = src.Proxy
	} else {
		run.State = "running"
	}
	if !s.diags.add(run) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a diagnostic is still running from " + src.Name + "; try again when it ends"})
		return
	}
	s.diagLimit.Fail(key)
	detail := ""
	switch dreq.Tool {
	case "ping":
		detail = strconv.Itoa(dreq.Count) + " echoes"
	case "dns":
		detail = dreq.Type
		if dreq.Type == "both" {
			detail = "A + AAAA"
		}
		if dreq.Server != "" {
			detail += ", asking " + dreq.Server
		}
	}
	noteChange(r, store.Change{Category: "probes", Action: "Ran a diagnostic",
		Object: diagToolName[dreq.Tool] + " " + dreq.Target + " from " + src.Name, Detail: detail, HostIDs: hostIDs})
	if src.Proxy == "" {
		go s.runCoreDiag(run.ID, dreq)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": run.ID})
}

// runCoreDiag runs a core run in process, posting each partial result to the board.
func (s *Server) runCoreDiag(id int64, req diag.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), diagRunLimit)
	defer cancel()
	res := diag.Run(ctx, req, func(p diag.Result) { s.diags.report("core", id, p) })
	s.diags.report("core", id, res)
}

// GET /api/diag/{id} (admin, helpdesk): a run so far. Only the one who started it reads it (an admin
// reads any).
func (s *Server) handleGetDiag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, found := s.diags.get(id)
	u, _ := auth.UserFrom(r.Context())
	if !found || (run.By != u.ID && u.Role != "admin") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that diagnostic is gone (they are kept for 15 minutes)"})
		return
	}
	writeJSON(w, http.StatusOK, viewDiag(run))
}

// POST /api/diag/warm (admin, helpdesk): the dialog is open on a probe, so that probe checks in fast
// for a while and a run starts within seconds. Body: {source}. The core needs no warming.
func (s *Server) handleWarmDiag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
	}
	if !decode(w, r, &req) {
		return
	}
	if id := strings.TrimSpace(req.Source); id != "" && id != "core" {
		ctx, cancel := scopeLookupCtx(r)
		defer cancel()
		if src, _, why := s.resolveDiagSource(ctx, r, id); why == "" {
			s.diags.warmUp(src.Proxy)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/probes/diag-results (public; probe-token authenticated): a probe reports on the run its
// check-in handed it, partial results as they come and the last with done set.
func (s *Server) handleDiagResults(w http.ResponseWriter, r *http.Request) {
	tok := bearerToken(r)
	if tok == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing probe token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	proxyName, err := s.st.ProbeNameByToken(ctx, auth.HashToken(tok))
	if err != nil {
		s.logger.Warn("diagnostic result refused: unknown probe token", "ip", s.clientIP(r))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid probe token"})
		return
	}
	var req struct {
		ID     int64       `json:"id"`
		Result diag.Result `json:"result"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		s.logger.Warn("diagnostic result refused: unreadable body", "probe", proxyName, "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if why := s.diags.report(proxyName, req.ID, cleanDiagResult(req.Result)); why != "" {
		s.logger.Warn("diagnostic result refused", "probe", proxyName, "id", req.ID, "why", why)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown diagnostic: " + why})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanDiagResult bounds what a probe reported: lengths, counts, and no control characters in
// anything shown on one line (the raw output keeps its line breaks).
func cleanDiagResult(res diag.Result) diag.Result {
	line := func(s string, n int) string { return clip(printable(s), n) }
	res.Addr, res.Error = line(res.Addr, 64), line(res.Error, 300)
	raw := []rune(strings.Map(func(c rune) rune {
		if c == '\n' || c == '\t' || c >= 0x20 && c != 0x7f {
			return c
		}
		return -1
	}, res.Raw))
	res.Raw = string(raw[:min(len(raw), 32<<10)])
	if p := res.Ping; p != nil {
		p.Replies = p.Replies[:min(len(p.Replies), 20)]
		for i := range p.Replies {
			p.Replies[i].From, p.Replies[i].Error = line(p.Replies[i].From, 64), line(p.Replies[i].Error, 160)
		}
	}
	if t := res.Trace; t != nil {
		t.Hops = t.Hops[:min(len(t.Hops), diag.MaxHops)]
		for i := range t.Hops {
			h := &t.Hops[i]
			h.Addr, h.Name, h.Note = line(h.Addr, 64), line(h.Name, 253), line(h.Note, 80)
		}
	}
	if d := res.DNS; d != nil {
		d.Server = line(d.Server, 64)
		for _, as := range []*[]diag.Answer{&d.Answers, &d.System} {
			*as = (*as)[:min(len(*as), 50)]
			for i := range *as {
				a := &(*as)[i]
				a.Type, a.Value, a.Error = line(a.Type, 8), line(a.Value, 512), line(a.Error, 200)
			}
		}
	}
	return res
}
