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
const probeProcsColumns = `procs_running,procs_pinned,procs_since,procs_target,procs_peaks,procs_note,procs_decided_at,procs_restart_at,restarts`

// probeProcsScan holds the raw columns until they are decoded.
type probeProcsScan struct {
	running, pinned, target, peaks string
	restarts                       int
}

func (r *probeProcsScan) dest(p *ProbeProcs) []any {
	return []any{&r.running, &r.pinned, &p.Since, &r.target, &r.peaks, &p.Note, &p.DecidedAt, &p.RestartAt, &r.restarts}
}

func (r *probeProcsScan) decode(p *ProbeProcs) {
	_ = json.Unmarshal([]byte(orJSON(r.running, "{}")), &p.Running)
	_ = json.Unmarshal([]byte(orJSON(r.pinned, "[]")), &p.Pinned)
	_ = json.Unmarshal([]byte(orJSON(r.target, "null")), &p.Target)
	_ = json.Unmarshal([]byte(orJSON(r.peaks, "{}")), &p.Peaks)
	p.Restarts = r.restarts != 0
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

// SetProbeProcsEvaluation records one evaluation: the busiest hourly average per process, and,
// when Argus decided to change them, the new target and the change in words.
func (s *Store) SetProbeProcsEvaluation(ctx context.Context, proxyName string, peaks map[string]float64, target map[string]int, note string, changed bool) error {
	peakJSON, _ := json.Marshal(peaks)
	if !changed {
		_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET procs_peaks=? WHERE proxy_name=?`, string(peakJSON), proxyName)
		return err
	}
	tgtJSON, _ := json.Marshal(target)
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET procs_peaks=?, procs_target=?, procs_note=?, procs_decided_at=? WHERE proxy_name=?`,
		string(peakJSON), string(tgtJSON), note, time.Now().Unix(), proxyName)
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
