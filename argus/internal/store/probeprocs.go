// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// ProbeProcs is a probe's Zabbix process counts (StartPingers, StartPollers, ...) as Argus manages
// them. Zabbix reads them only when the proxy starts, so Argus keeps a target per probe, hands it
// out at check-in, and the probe starts with it.
type ProbeProcs struct {
	Running   map[string]int     // what the proxy started with, reported at every check-in
	Pinned    []string           // counts set on the container itself: they win, Argus leaves them
	Since     int64              // when Argus first saw the current running counts (unix)
	Target    map[string]int     // what Argus wants the probe to run (nil until the first report)
	Peaks     map[string]float64 // busiest hourly average per process at the last evaluation
	Note      string             // the last change, in words
	DecidedAt int64              // when Argus last changed the target (unix)
	RestartAt int64              // when Argus last asked the updater sidecar to restart the probe (unix)
	Restarts  bool               // the probe's updater sidecar can restart it on request
	// Changes are the raises still to be judged: at the first evaluation after one is applied, Argus
	// checks that the load came down; Held are the kinds whose raise didn't help, put back and not
	// raised again until the CPU count changes or an admin releases them.
	Changes map[string]ProcChange
	Held    map[string]ProcHold
	CPU     ProbeCPU
}

// ProcChange is a raise waiting to be judged.
type ProcChange struct {
	From int     `json:"from"`
	To   int     `json:"to"`
	Peak float64 `json:"peak"` // the busiest hour that led to it
	At   int64   `json:"at"`
}

// ProcHold is a kind put back after a raise that didn't lower its load.
type ProcHold struct {
	At     int64   `json:"at"`
	CPUs   float64 `json:"cpus"` // the CPUs the probe had then (0 = unknown); a different count lifts it
	From   int     `json:"from"`
	To     int     `json:"to"`
	Before float64 `json:"before"` // busiest hour before the raise
	After  float64 `json:"after"`  // and after it
}

// ProbeCPU is what the probe reports about its CPU at check-in, plus the last evaluation.
type ProbeCPU struct {
	Count   int     // CPUs the container sees
	Quota   float64 // a container CPU limit, in CPUs (0 = none)
	Load1   float64
	Load5   float64
	Load15  float64
	At      int64   // last report (unix; 0 = never)
	Peak    float64 // busiest hourly load average per CPU at the last evaluation (-1 = unknown)
	Starved bool    // at the last evaluation the probe was short on CPU
}

// Effective is the number of CPUs the probe can use: a container limit when it has one below the
// count, else the count (0 = unknown).
func (c ProbeCPU) Effective() float64 {
	if c.Quota > 0 && (c.Count == 0 || c.Quota < float64(c.Count)) {
		return c.Quota
	}
	return float64(c.Count)
}

// Pending reports whether the probe still runs other counts than the target (a change waiting
// for the probe's next start).
func (p ProbeProcs) Pending() bool {
	for k, v := range p.Target {
		if p.Running[k] != v {
			return true
		}
	}
	return false
}

// probeProcsColumns are the ProbeProcs columns, in scanProbeProcs order.
const probeProcsColumns = `procs_running,procs_pinned,procs_since,procs_target,procs_peaks,procs_note,procs_decided_at,procs_restart_at,restarts,` +
	`procs_changes,procs_held,cpu_count,cpu_quota,load1,load5,load15,cpu_at,cpu_peak,cpu_starved`

// probeProcsScan holds the raw columns until they are decoded.
type probeProcsScan struct {
	running, pinned, target, peaks, changes, held string
	restarts, starved                             int
}

func (r *probeProcsScan) dest(p *ProbeProcs) []any {
	return []any{&r.running, &r.pinned, &p.Since, &r.target, &r.peaks, &p.Note, &p.DecidedAt, &p.RestartAt, &r.restarts,
		&r.changes, &r.held, &p.CPU.Count, &p.CPU.Quota, &p.CPU.Load1, &p.CPU.Load5, &p.CPU.Load15, &p.CPU.At, &p.CPU.Peak, &r.starved}
}

func (r *probeProcsScan) decode(p *ProbeProcs) {
	_ = json.Unmarshal([]byte(orJSON(r.running, "{}")), &p.Running)
	_ = json.Unmarshal([]byte(orJSON(r.pinned, "[]")), &p.Pinned)
	_ = json.Unmarshal([]byte(orJSON(r.target, "null")), &p.Target)
	_ = json.Unmarshal([]byte(orJSON(r.peaks, "{}")), &p.Peaks)
	_ = json.Unmarshal([]byte(orJSON(r.changes, "{}")), &p.Changes)
	_ = json.Unmarshal([]byte(orJSON(r.held, "{}")), &p.Held)
	p.Restarts = r.restarts != 0
	p.CPU.Starved = r.starved != 0
}

func orJSON(s, empty string) string {
	if s == "" {
		return empty
	}
	return s
}

// RecordProbeProcs stores the process counts a probe reports it started with, and which of them
// the operator pinned on the container. A change of the running counts restarts the settle clock
// (Since). The first report seeds the target with what is running, so nothing changes until Argus
// has watched the load; a count that became pinned leaves the target, one that is new joins it.
func (s *Store) RecordProbeProcs(ctx context.Context, proxyName string, running map[string]int, pinned []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var curRunning, curTarget string
	if err := tx.QueryRowContext(ctx, `SELECT procs_running,procs_target FROM probe_agents WHERE proxy_name=?`, proxyName).
		Scan(&curRunning, &curTarget); err != nil {
		return err
	}
	sort.Strings(pinned)
	isPinned := make(map[string]bool, len(pinned))
	for _, n := range pinned {
		isPinned[n] = true
	}
	var target map[string]int
	_ = json.Unmarshal([]byte(orJSON(curTarget, "null")), &target)
	if target == nil {
		target = map[string]int{}
	}
	for n := range target {
		if isPinned[n] {
			delete(target, n)
		}
	}
	for n, v := range running {
		if _, ok := target[n]; !ok && !isPinned[n] {
			target[n] = v
		}
	}
	runJSON, _ := json.Marshal(running) // map keys marshal sorted: a stable string to compare
	pinJSON, _ := json.Marshal(pinned)
	tgtJSON, _ := json.Marshal(target)
	if string(runJSON) != curRunning {
		_, err = tx.ExecContext(ctx, `UPDATE probe_agents SET procs_running=?, procs_since=?, procs_pinned=?, procs_target=? WHERE proxy_name=?`,
			string(runJSON), time.Now().Unix(), string(pinJSON), string(tgtJSON), proxyName)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE probe_agents SET procs_pinned=?, procs_target=? WHERE proxy_name=?`,
			string(pinJSON), string(tgtJSON), proxyName)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ProcsEvaluation is one evaluation of a probe's process counts.
type ProcsEvaluation struct {
	Peaks   map[string]float64    // busiest hourly average per kind
	Target  map[string]int        // the counts Argus wants now
	Changes map[string]ProcChange // raises still to be judged
	Held    map[string]ProcHold   // kinds held after a raise that didn't help
	CPUPeak float64               // busiest hourly load average per CPU (-1 = unknown)
	Starved bool                  // short on CPU: no raises
	Note    string                // what changed, in words ("" = nothing)
	At      int64                 // when (unix; 0 = now)
}

// SetProbeProcsEvaluation records one evaluation. A note means something changed (a count, a hold):
// it becomes the last change, with the time, for the Probes page and the system notice.
func (s *Store) SetProbeProcsEvaluation(ctx context.Context, proxyName string, ev ProcsEvaluation) error {
	peakJSON, _ := json.Marshal(ev.Peaks)
	tgtJSON, _ := json.Marshal(ev.Target)
	chJSON, _ := json.Marshal(ev.Changes)
	heldJSON, _ := json.Marshal(ev.Held)
	starved := 0
	if ev.Starved {
		starved = 1
	}
	set := `procs_peaks=?, procs_target=?, procs_changes=?, procs_held=?, cpu_peak=?, cpu_starved=?`
	args := []any{string(peakJSON), string(tgtJSON), string(chJSON), string(heldJSON), ev.CPUPeak, starved}
	if ev.Note != "" {
		at := ev.At
		if at == 0 {
			at = time.Now().Unix()
		}
		set += `, procs_note=?, procs_decided_at=?`
		args = append(args, ev.Note, at)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET `+set+` WHERE proxy_name=?`, append(args, proxyName)...)
	return err
}

// ReleaseProbeProcHolds clears a probe's holds and unjudged raises (an admin's "try again").
func (s *Store) ReleaseProbeProcHolds(ctx context.Context, proxyName string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET procs_held='', procs_changes='', cpu_starved=0, procs_note=?, procs_decided_at=? WHERE proxy_name=?`,
		"Holds released by an admin; Argus judges the counts again", time.Now().Unix(), proxyName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadSample is one load-average report.
type LoadSample struct {
	At    int64
	Load1 float64
	CPUs  float64
}

// RecordProbeCPU stores the CPU a probe reports at check-in and keeps the sample. A change in the
// CPUs it can use (a VM given more vCPUs, a container limit changed) restarts the settle clock:
// the load seen before no longer says anything about the counts.
func (s *Store) RecordProbeCPU(ctx context.Context, proxyName string, count int, quota, load1, load5, load15 float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prev ProbeCPU
	if err := tx.QueryRowContext(ctx, `SELECT cpu_count,cpu_quota FROM probe_agents WHERE proxy_name=?`, proxyName).Scan(&prev.Count, &prev.Quota); err != nil {
		return err
	}
	cur := ProbeCPU{Count: count, Quota: quota}
	now := time.Now().Unix()
	set := `cpu_count=?, cpu_quota=?, load1=?, load5=?, load15=?, cpu_at=?`
	args := []any{count, quota, load1, load5, load15, now}
	if prev.Effective() > 0 && cur.Effective() != prev.Effective() {
		set += `, procs_since=?`
		args = append(args, now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_agents SET `+set+` WHERE proxy_name=?`, append(args, proxyName)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_load(proxy_name,at,load1,cpus) VALUES(?,?,?,?)`, proxyName, now, load1, cur.Effective()); err != nil {
		return err
	}
	return tx.Commit()
}

// ProbeLoadSince returns a probe's load samples from a time on, oldest first.
func (s *Store) ProbeLoadSince(ctx context.Context, proxyName string, from int64) ([]LoadSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at,load1,cpus FROM probe_load WHERE proxy_name=? AND at>=? ORDER BY at`, proxyName, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoadSample
	for rows.Next() {
		var l LoadSample
		if err := rows.Scan(&l.At, &l.Load1, &l.CPUs); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PruneProbeLoad drops load samples older than a time.
func (s *Store) PruneProbeLoad(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM probe_load WHERE at<?`, before)
	return err
}

// ClaimProbeRestart takes the right to restart a probe now, at most once per gap: the one check-in
// whose update moves procs_restart_at gets it.
func (s *Store) ClaimProbeRestart(ctx context.Context, proxyName string, gap time.Duration) (bool, error) {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET procs_restart_at=? WHERE proxy_name=? AND procs_restart_at<=?`,
		now.Unix(), proxyName, now.Add(-gap).Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetProbeRestarts records whether the probe's updater sidecar can restart it on request.
func (s *Store) SetProbeRestarts(ctx context.Context, proxyName string, restarts bool) error {
	v := 0
	if restarts {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET restarts=? WHERE proxy_name=?`, v, proxyName)
	return err
}
