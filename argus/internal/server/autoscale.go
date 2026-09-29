// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
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
	// A raise is judged at the first evaluation after it applied: it helped when the busiest hour
	// came down by at least this share of what it predicted (5 -> 8 at 72% predicts 45%).
	procHelped = 1.0 / 3
	// The busiest hour's load average per usable CPU at or above this: the machine is short on CPU,
	// and more processes can only add to the queue for it.
	procCPUFull      = 1.0
	procLoadHourMin  = 30 // samples (one a minute) an hour needs to count
	procLoadKeep     = 26 * time.Hour
	procLoadMaxCount = 4096 // sanity bounds for what a probe reports
	procLoadMax      = 100000.0
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
	_ = s.st.PruneProbeLoad(ctx, now.Add(-procLoadKeep).Unix())
	for name, ag := range agents {
		p := ag.Procs
		if len(p.Target) == 0 || p.Pending() || now.Sub(time.Unix(p.Since, 0)) < procSettle {
			continue
		}
		peaks, cpuPeak, cpuHours, ok := s.measureProcs(ctx, name, p, now)
		if !ok {
			continue
		}
		ev := planProcs(p, peaks, cpuPeak, cpuHours, now)
		if err := s.st.SetProbeProcsEvaluation(ctx, name, ev); err != nil {
			s.logger.Warn("probe autoscale: could not store the evaluation", "proxy", name, "err", err)
			continue
		}
		if ev.Note != "" {
			s.logger.Info("probe autoscale: process counts", "proxy", name, "change", ev.Note)
		}
	}
}

// measureProcs reads what a probe's counts are judged on, since they last changed (at most a day
// back): each kind's busiest hourly average from Zabbix trends, and the busiest hour's load average
// per CPU from the probe's own reports (hours = how many hours of those there are).
func (s *Server) measureProcs(ctx context.Context, proxyName string, p store.ProbeProcs, now time.Time) (peaks map[string]float64, cpuPeak float64, cpuHours int, ok bool) {
	hostID, err := s.zbx.HostIDByName(ctx, probeHostName(proxyName))
	if err != nil || hostID == "" {
		return nil, 0, 0, false
	}
	items, err := s.zbx.Items(ctx, hostID)
	if err != nil {
		return nil, 0, 0, false
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
	for _, sp := range procSpecs {
		id := byKey[sp.busyKey()]
		if _, managed := p.Target[sp.Name]; !managed || id == "" {
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
	}
	samples, _ := s.st.ProbeLoadSince(ctx, proxyName, from)
	cpuPeak, cpuHours = busiestLoadPerCPU(samples)
	return peaks, cpuPeak, cpuHours, true
}

// busiestLoadPerCPU is the highest hourly average of the load average per usable CPU, and how many
// hours had enough samples to count (-1, 0 when none did).
func busiestLoadPerCPU(samples []store.LoadSample) (peak float64, hours int) {
	type acc struct {
		sum float64
		n   int
	}
	byHour := map[int64]*acc{}
	for _, sm := range samples {
		if sm.CPUs <= 0 {
			continue
		}
		h := sm.At / 3600
		a := byHour[h]
		if a == nil {
			a = &acc{}
			byHour[h] = a
		}
		a.sum += sm.Load1 / sm.CPUs
		a.n++
	}
	peak = -1
	for _, a := range byHour {
		if a.n < procLoadHourMin {
			continue
		}
		hours++
		if v := a.sum / float64(a.n); v > peak {
			peak = v
		}
	}
	if peak >= 0 {
		peak = math.Round(peak*100) / 100
	}
	return peak, hours
}

// raiseHelped reports whether a raise brought the busiest hour down by enough of what it predicted.
func raiseHelped(ch store.ProcChange, peak float64) bool {
	if ch.To <= ch.From || ch.Peak <= 0 {
		return true
	}
	predicted := ch.Peak * float64(ch.From) / float64(ch.To)
	return ch.Peak-peak >= (ch.Peak-predicted)*procHelped
}

// planProcs decides one evaluation of a probe's counts from its measurements:
//   - a different usable CPU count than when a kind was held releases the hold (the machine changed);
//   - the first judgement after a raise: one that didn't bring the load down enough is put back and
//     the kind held (not raised again), since the limit was elsewhere;
//   - short on CPU (the busiest hour's load average reached the CPUs it can use): no raises;
//   - otherwise each kind follows decideProcs, and every raise is remembered to be judged.
func planProcs(p store.ProbeProcs, peaks map[string]float64, cpuPeak float64, cpuHours int, now time.Time) store.ProcsEvaluation {
	ev := store.ProcsEvaluation{
		Peaks: peaks, Target: map[string]int{}, Changes: map[string]store.ProcChange{}, Held: map[string]store.ProcHold{},
		CPUPeak: cpuPeak, At: now.Unix(),
	}
	for k, v := range p.Target {
		ev.Target[k] = v
	}
	for k, v := range p.Changes {
		if _, managed := ev.Target[k]; managed {
			ev.Changes[k] = v
		}
	}
	for k, v := range p.Held {
		if _, managed := ev.Target[k]; managed {
			ev.Held[k] = v
		}
	}
	cpus := p.CPU.Effective()
	var notes, lifted []string
	for _, sp := range procSpecs {
		if h, held := ev.Held[sp.Name]; held && h.CPUs > 0 && cpus > 0 && cpus != h.CPUs {
			delete(ev.Held, sp.Name)
			lifted = append(lifted, sp.Label)
		}
	}
	if len(lifted) > 0 {
		notes = append(notes, fmt.Sprintf("%s released: the probe now has %s CPUs, so Argus judges them again", strings.Join(lifted, ", "), fmtCPUs(cpus)))
	}
	ev.Starved = cpuHours >= procMinHours && cpuPeak >= procCPUFull
	for _, sp := range procSpecs {
		cur, managed := ev.Target[sp.Name]
		peak, measured := peaks[sp.Name]
		if !managed || !measured {
			continue
		}
		if ch, pending := ev.Changes[sp.Name]; pending && cur == ch.To {
			delete(ev.Changes, sp.Name)
			if !raiseHelped(ch, peak) {
				ev.Target[sp.Name] = ch.From
				ev.Held[sp.Name] = store.ProcHold{At: now.Unix(), CPUs: cpus, From: ch.From, To: ch.To, Before: ch.Peak, After: peak}
				notes = append(notes, fmt.Sprintf("%s back to %d: raising them to %d didn't lower their load (busiest hour %.0f%% before, %.0f%% after)",
					sp.Label, ch.From, ch.To, ch.Peak, peak))
				continue
			}
		}
		if _, held := ev.Held[sp.Name]; held {
			continue
		}
		t := decideProcs(sp, cur, peak)
		if t > cur && ev.Starved {
			continue // more processes can't help a machine that is out of CPU
		}
		if t == cur {
			continue
		}
		ev.Target[sp.Name] = t
		if t > cur {
			ev.Changes[sp.Name] = store.ProcChange{From: cur, To: t, Peak: peak, At: now.Unix()}
		} else {
			delete(ev.Changes, sp.Name)
		}
		notes = append(notes, fmt.Sprintf("%s %d -> %d (busiest hour %.0f%%)", sp.Label, cur, t, peak))
	}
	ev.Note = strings.Join(notes, "; ")
	return ev
}

// fmtCPUs writes a CPU count without a needless decimal (2, 1.5).
func fmtCPUs(c float64) string {
	return strconv.FormatFloat(c, 'f', -1, 64)
}

// cleanCPU checks what a probe reports about its CPU; ok is false for anything out of shape.
func cleanCPU(count int, quota float64, load []float64) (int, float64, [3]float64, bool) {
	var l [3]float64
	if count < 0 || count > procLoadMaxCount || quota < 0 || quota > procLoadMaxCount || len(load) != 3 {
		return 0, 0, l, false
	}
	for i, v := range load {
		if v < 0 || v > procLoadMax || math.IsNaN(v) {
			return 0, 0, l, false
		}
		l[i] = v
	}
	return count, quota, l, true
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

// probeCPUNotice words the "short on CPU" condition for a probe: what Argus saw, and what to do,
// for a VM (more vCPUs) or a container (the Docker host, whose load includes other containers).
func probeCPUNotice(site string, ag store.ProbeAgent) (title, detail string) {
	p := ag.Procs
	var parts []string
	if p.CPU.Starved && p.CPU.Peak >= 0 {
		parts = append(parts, fmt.Sprintf("Its busiest hour averaged a load of %.2f per CPU (%s CPUs usable), so Argus adds no processes.", p.CPU.Peak, fmtCPUs(p.CPU.Effective())))
	}
	var held []string
	for _, sp := range procSpecs {
		if _, ok := p.Held[sp.Name]; ok {
			held = append(held, strings.ToLower(sp.Label))
		}
	}
	if len(held) > 0 {
		parts = append(parts, "Raising its "+strings.Join(held, ", ")+" didn't lower their load, so Argus put them back and holds them (or the site's load grew while the change ran).")
	}
	if ag.OSReportedAt > 0 { // a probe VM (its host-side reporter posts OS status)
		parts = append(parts, "Give the VM more vCPUs, or split the site across two probes.")
	} else {
		parts = append(parts, "The Docker host it runs on may be short on CPU (the load average there includes its other containers), or the site needs a second probe.")
	}
	parts = append(parts, "Argus tries again when the CPU count changes, or from the probe's Processes panel.")
	return "Probe " + site + " may be short on CPU", strings.Join(parts, " ")
}

// handleReleaseProcHolds lifts a probe's holds (admin): Argus judges its counts again, and may
// raise a kind it had put back.
func (s *Server) handleReleaseProcHolds(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.st.ReleaseProbeProcHolds(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this probe hasn't checked in to Argus"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not release the holds"})
		return
	}
	s.logger.Info("probe autoscale: holds released", "proxy", name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
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
	Name    string          `json:"name"`
	Label   string          `json:"label"`
	Running int             `json:"running"`
	Target  int             `json:"target,omitempty"` // 0 when pinned or not managed
	Pinned  bool            `json:"pinned,omitempty"`
	Peak    *float64        `json:"peak,omitempty"` // busiest hourly average at the last evaluation
	Held    *store.ProcHold `json:"held,omitempty"` // put back after a raise that didn't help
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
		if h, ok := p.Held[sp.Name]; ok {
			h := h
			v.Held = &h
		}
		out = append(out, v)
	}
	return out
}
