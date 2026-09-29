// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"argus/internal/settings"
	"argus/internal/store"
)

// Probe process autoscaling. A Zabbix proxy starts a fixed number of each process kind (pingers,
// pollers, trappers, workers) and reads the numbers only at start. Every probe's Probe health host
// records how busy each kind is, so Argus sizes them: it watches the busiest hourly average over
// the last day at the current counts, raises a count whose busiest hour reached procRaiseAt (so the
// load lands near procTargetBusy), lowers one that stayed under procLowerAt, never below the
// image's own default and never above a per-kind ceiling. The new counts go out at check-in; the
// probe starts with them, and the updater sidecar restarts it to apply them when the mode allows.

// procSpec is one process kind Argus sizes.
type procSpec struct {
	Name  string // the Zabbix option; the probe's variable is ZBX_ + its upper case (ZBX_STARTPINGERS)
	Label string
	Proc  string // the process type in zabbix[process,<type>,avg,busy]
	Min   int    // the probe image's default: never lower than this
	Max   int    // a ceiling: past it, a busy kind means the site needs a second probe, not more forks
}

var procSpecs = []procSpec{
	{"StartPingers", "ICMP pingers", "icmp pinger", 5, 50},
	{"StartPollers", "Pollers", "poller", 5, 100},
	{"StartPollersUnreachable", "Unreachable pollers", "unreachable poller", 1, 20},
	{"StartAgentPollers", "Agent pollers", "agent poller", 1, 10},
	{"StartSNMPPollers", "SNMP pollers", "snmp poller", 1, 10},
	{"StartHTTPAgentPollers", "HTTP agent pollers", "http agent poller", 1, 10},
	{"StartTrappers", "Trappers", "trapper", 5, 50},
	// History syncers write the proxy's SQLite buffer; more than a few only contend for its lock.
	{"StartDBSyncers", "History syncers", "history syncer", 4, 8},
	{"StartPreprocessors", "Preprocessing workers", "preprocessing worker", 16, 64},
}

func procSpecByName(name string) (procSpec, bool) {
	for _, sp := range procSpecs {
		if sp.Name == name {
			return sp, true
		}
	}
	return procSpec{}, false
}

// busyKey is the Probe health item that measures this kind, exactly as the template writes it.
func (sp procSpec) busyKey() string {
	t := sp.Proc
	if strings.Contains(t, " ") {
		t = `"` + t + `"`
	}
	return "zabbix[process," + t + ",avg,busy]"
}

const (
	procTargetBusy = 50.0 // where a changed count aims the busiest hour
	procRaiseAt    = 60.0 // busiest hour at or above this raises the count (the warning is at 75%)
	procLowerAt    = 20.0 // busiest hour at or below this lowers it (down to the default)
	procSettle     = 6 * time.Hour
	procWindow     = 24 * time.Hour
	procMinHours   = 3             // hourly averages needed before a kind is judged
	procRestartGap = 6 * time.Hour // at most one restart per probe in this long
	procEvery      = 15 * time.Minute
	procMaxCount   = 1000 // Zabbix's own ceiling for every one of these options
)

// decideProcs is the new count for a kind now running cur processes whose busiest hourly average
// was peak percent. It is cur when nothing should change.
func decideProcs(sp procSpec, cur int, peak float64) int {
	if cur <= 0 {
		return cur
	}
	need := int(math.Ceil(float64(cur) * peak / procTargetBusy))
	switch {
	case peak >= procRaiseAt:
		t := need
		if t <= cur {
			t = cur + 1
		}
		if t > sp.Max {
			t = sp.Max
		}
		if t < cur { // already above the ceiling: leave it
			t = cur
		}
		return t
	case peak <= procLowerAt && cur > sp.Min:
		t := need
		if t < sp.Min {
			t = sp.Min
		}
		if t >= cur {
			return cur
		}
		return t
	}
	return cur
}

// startProcAutoscale evaluates every probe's process load in the background.
func (s *Server) startProcAutoscale(ctx context.Context) {
	go func() {
		first := time.NewTimer(3 * time.Minute) // let Zabbix and the probes' first check-ins come in
		defer first.Stop()
		t := time.NewTicker(procEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			s.procAutoscaleTick(c)
			cancel()
		}
	}()
}

// procAutoscaleTick evaluates each probe that reports its counts, has run them for procSettle and
// has no change waiting to be applied.
func (s *Server) procAutoscaleTick(ctx context.Context) {
	if s.mgr.ProbeAutoscale() == settings.AutoscaleOff || !s.zbx.Authenticated() {
		return
	}
	agents, err := s.st.ProbeAgents(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for name, ag := range agents {
		p := ag.Procs
		if len(p.Target) == 0 || p.Pending() || now.Sub(time.Unix(p.Since, 0)) < procSettle {
			continue
		}
		peaks, target, changes, ok := s.evaluateProcs(ctx, name, p, now)
		if !ok {
			continue
		}
		note := strings.Join(changes, "; ")
		if err := s.st.SetProbeProcsEvaluation(ctx, name, peaks, target, note, len(changes) > 0); err != nil {
			s.logger.Warn("probe autoscale: could not store the evaluation", "proxy", name, "err", err)
			continue
		}
		if len(changes) > 0 {
			s.logger.Info("probe autoscale: new process counts", "proxy", name, "changes", note)
		}
	}
}

// evaluateProcs reads a probe's busiest hourly averages since its counts last changed (at most a
// day back) and decides each kind.
func (s *Server) evaluateProcs(ctx context.Context, proxyName string, p store.ProbeProcs, now time.Time) (peaks map[string]float64, target map[string]int, changes []string, ok bool) {
	hostID, err := s.zbx.HostIDByName(ctx, probeHostName(proxyName))
	if err != nil || hostID == "" {
		return nil, nil, nil, false
	}
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		return nil, nil, nil, false
	}
	byKey := make(map[string]string, len(items))
	for _, it := range items {
		byKey[it.Key] = it.ItemID
	}
	from := now.Add(-procWindow).Unix()
	if p.Since > from {
		from = p.Since
	}
	peaks = map[string]float64{}
	target = make(map[string]int, len(p.Target))
	for k, v := range p.Target {
		target[k] = v
	}
	for _, sp := range procSpecs {
		cur, managed := p.Target[sp.Name]
		id := byKey[sp.busyKey()]
		if !managed || id == "" {
			continue
		}
		pts, err := s.zbx.Trends(ctx, id, from, now.Unix())
		if err != nil || len(pts) < procMinHours {
			continue
		}
		peak := 0.0
		for _, pt := range pts {
			if v, err := strconv.ParseFloat(pt.ValueAvg, 64); err == nil && v > peak {
				peak = v
			}
		}
		peaks[sp.Name] = math.Round(peak*10) / 10
		if t := decideProcs(sp, cur, peak); t != cur {
			target[sp.Name] = t
			changes = append(changes, fmt.Sprintf("%s %d -> %d (busiest hour %.0f%%)", sp.Label, cur, t, peak))
		}
	}
	return peaks, target, changes, true
}

// procsHandout fills a check-in answer with the probe's target counts (every caller: the proxy's
// start-time check-in is the one that applies them) and, for a sidecar that can restart the proxy
// while a change waits and the mode allows, a one-shot restart, at most once per procRestartGap.
func (s *Server) procsHandout(ctx context.Context, proxyName string, selfUpdate, restarts *bool, procs *map[string]int, restart *bool) {
	mode := s.mgr.ProbeAutoscale()
	if mode == settings.AutoscaleOff {
		return
	}
	ag, err := s.st.ProbeAgentByName(ctx, proxyName)
	if err != nil || len(ag.Procs.Target) == 0 {
		return
	}
	*procs = ag.Procs.Target
	if mode != settings.AutoscaleRestart || selfUpdate == nil || !*selfUpdate || restarts == nil || !*restarts || !ag.Procs.Pending() {
		return
	}
	if ok, _ := s.st.ClaimProbeRestart(ctx, proxyName, procRestartGap); ok {
		*restart = true
		s.logger.Info("probe autoscale: asking the updater to restart the probe", "proxy", proxyName, "change", ag.Procs.Note)
	}
}

// cleanProcs keeps the counts and pins a probe reports for the kinds Argus knows, in Zabbix's range.
func cleanProcs(running map[string]int, pinned []string) (map[string]int, []string) {
	out := map[string]int{}
	for n, v := range running {
		if _, known := procSpecByName(n); known && v >= 0 && v <= procMaxCount {
			out[n] = v
		}
	}
	var pins []string
	seen := map[string]bool{}
	for _, n := range pinned {
		if _, known := procSpecByName(n); known && !seen[n] {
			pins = append(pins, n)
			seen[n] = true
		}
	}
	return out, pins
}

// procView is one process kind on the Probes page.
type procView struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Running int      `json:"running"`
	Target  int      `json:"target,omitempty"` // 0 when pinned or not managed
	Pinned  bool     `json:"pinned,omitempty"`
	Peak    *float64 `json:"peak,omitempty"` // busiest hourly average at the last evaluation
}

// procViews lists a probe's process kinds in the spec order ("" when it reports none).
func procViews(p store.ProbeProcs) []procView {
	if len(p.Running) == 0 {
		return nil
	}
	pinned := map[string]bool{}
	for _, n := range p.Pinned {
		pinned[n] = true
	}
	var out []procView
	for _, sp := range procSpecs {
		run, reported := p.Running[sp.Name]
		if !reported {
			continue
		}
		v := procView{Name: sp.Name, Label: sp.Label, Running: run, Target: p.Target[sp.Name], Pinned: pinned[sp.Name]}
		if pk, ok := p.Peaks[sp.Name]; ok {
			pk := pk
			v.Peak = &pk
		}
		out = append(out, v)
	}
	return out
}
