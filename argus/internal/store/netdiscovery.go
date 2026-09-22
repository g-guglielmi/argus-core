// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Network auto-discovery (§B): an admin queues a subnet scan for a probe; the job is handed out
// once at the probe's next check-in (pending -> dispatched) and completed when the probe posts the
// scan results back. Results are the raw per-host fingerprints, kept per job so the Discovery view
// can review, adopt or ignore them.

// ErrDiscoveryBusy is returned when a source's scan queue is full.
var ErrDiscoveryBusy = errors.New("too many scans queued for this source")

// Scans queue per source (proxy name, or "" = the core server) and run one at a time - the probe's
// scanner is lock-file serialised anyway, and the core chains its queue in-process.
const (
	// discoveryQueueMax caps a source's pending+running scans.
	discoveryQueueMax = 5
	// discoveryPendingMaxAge fails a pending job nothing came to pick up (probe offline / not
	// scan-capable). It only applies while NOTHING of that source is dispatched - a job waiting in
	// line behind a running scan is healthy at any age.
	discoveryPendingMaxAge = 5 * time.Minute
	// discoveryDispatchedMaxAge fails a dispatched job whose results never came back (the
	// scanner's own budget is 8 minutes plus posting retries).
	discoveryDispatchedMaxAge = 15 * time.Minute
	// discoveryRetention is how long finished scans (and their results) are kept.
	discoveryRetention = 30 * 24 * time.Hour
	// discoveryKeepPerSource additionally caps stored scans per source within the retention window.
	discoveryKeepPerSource = 50
)

// DiscoveryJob is one queued discovery run: a subnet scan (kind "scan") or a UniFi controller
// sweep (kind "unifi", CIDR empty, ControllerID/ControllerName set). SNMPCommunity is decrypted
// only by TakeDiscoveryJob (the check-in handout); listing reads leave it empty.
type DiscoveryJob struct {
	ID             int64
	ProxyName      string
	Kind           string // scan | unifi
	ControllerID   int64  // unifi_controllers.id (sweep jobs)
	ControllerName string // display snapshot (sweep jobs)
	CIDR          string
	SNMPVersion   int
	SNMPCommunity string
	SNMPPort      int
	State         string // pending | dispatched | done | failed
	Error         string
	RequestedBy   string
	CreatedAt     int64
	DispatchedAt  int64
	CompletedAt   int64
	// Result counts, populated by ListDiscoveryJobs only (0 elsewhere).
	Found    int
	NewCount int
}

// DiscoveryResult is one fingerprinted host from a scan. TCPPorts and HTTPJSON hold the probe's
// raw JSON fragments (an int array / the banner object) - the store doesn't interpret them.
type DiscoveryResult struct {
	ID             int64
	JobID          int64
	IP             string
	MAC            string
	RDNS           string
	TCPPorts       string
	SysDescr       string
	SysObjectID    string
	SysName        string
	HTTPJSON       string
	DNS            bool
	SSHBanner      string
	UniFiJSON      string // controller-sourced facts (sweep results only)
	SuggestedClass string
	State          string // new | ignored | added
	HostID         string // Zabbix host id once adopted
}

const discoveryJobColumns = `id, proxy_name, kind, controller_id, controller_name, cidr, snmp_version, snmp_port, state, error, requested_by, created_at, dispatched_at, completed_at`

func scanDiscoveryJob(row interface{ Scan(...any) error }) (DiscoveryJob, error) {
	var j DiscoveryJob
	err := row.Scan(&j.ID, &j.ProxyName, &j.Kind, &j.ControllerID, &j.ControllerName, &j.CIDR,
		&j.SNMPVersion, &j.SNMPPort, &j.State, &j.Error,
		&j.RequestedBy, &j.CreatedAt, &j.DispatchedAt, &j.CompletedAt)
	return j, err
}

// expireStaleDiscoveryJobs fails jobs stuck in pending/dispatched past their grace periods, so a
// dead probe can never wedge its scan queue. Called lazily by every read/write below. A pending
// job is only stale while nothing of its source is dispatched - queued behind a running scan it
// waits as long as it takes.
func (s *Store) expireStaleDiscoveryJobs(ctx context.Context) {
	now := time.Now()
	_, _ = s.db.ExecContext(ctx,
		`UPDATE discovery_jobs SET state='failed', error='nothing picked the scan up - is the probe online and running a scan-capable image?', completed_at=?
		 WHERE state='pending' AND created_at < ?
		   AND NOT EXISTS (SELECT 1 FROM discovery_jobs d2 WHERE d2.proxy_name = discovery_jobs.proxy_name AND d2.state='dispatched')`,
		now.Unix(), now.Add(-discoveryPendingMaxAge).Unix())
	_, _ = s.db.ExecContext(ctx,
		`UPDATE discovery_jobs SET state='failed', error='the scan was picked up but never reported back', completed_at=?
		 WHERE state='dispatched' AND dispatched_at < ?`,
		now.Unix(), now.Add(-discoveryDispatchedMaxAge).Unix())
}

// CreateDiscoveryJob queues a scan (state pending). Scans queue per source: ErrDiscoveryBusy only
// when the source already has discoveryQueueMax scans pending/running. Finished scans older than
// the retention window (or beyond the per-source cap) are pruned with their results.
func (s *Store) CreateDiscoveryJob(ctx context.Context, j DiscoveryJob) (int64, error) {
	s.expireStaleDiscoveryJobs(ctx)
	var active int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM discovery_jobs WHERE proxy_name=? AND state IN ('pending','dispatched')`,
		j.ProxyName).Scan(&active); err != nil {
		return 0, err
	}
	if active >= discoveryQueueMax {
		return 0, ErrDiscoveryBusy
	}
	if j.Kind == "" {
		j.Kind = "scan"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO discovery_jobs(proxy_name, kind, controller_id, controller_name, cidr, snmp_version, snmp_community, snmp_port, state, error, requested_by, created_at, dispatched_at, completed_at)
		 VALUES(?,?,?,?,?,?,?,?,'pending','',?,?,0,0)`,
		j.ProxyName, j.Kind, j.ControllerID, j.ControllerName, j.CIDR,
		j.SNMPVersion, s.cipher.Encrypt(j.SNMPCommunity), j.SNMPPort,
		j.RequestedBy, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Prune: drop scans past the retention window, and anything beyond the newest per-source cap.
	// (The per-source branch is wrapped in a subselect - a bare ORDER BY/LIMIT would otherwise
	// apply to the whole UNION in SQLite.)
	cutoff := time.Now().Add(-discoveryRetention).Unix()
	stale := `SELECT id FROM discovery_jobs WHERE state IN ('done','failed') AND created_at < ?
	          UNION SELECT id FROM (SELECT id FROM discovery_jobs WHERE proxy_name=? ORDER BY id DESC LIMIT -1 OFFSET ?)`
	_, _ = s.db.ExecContext(ctx, `DELETE FROM discovery_results WHERE job_id IN (`+stale+`)`, cutoff, j.ProxyName, discoveryKeepPerSource)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM discovery_jobs WHERE id IN (`+stale+`)`, cutoff, j.ProxyName, discoveryKeepPerSource)
	return id, nil
}

// TakeDiscoveryJob hands a source its oldest pending scan job (one-shot: pending -> dispatched)
// with the SNMP community decrypted for the handout. nil when nothing is queued OR a scan of this
// source is already running - the queue drains strictly one at a time (the probe's scanner is
// lock-file serialised; the core chains its queue after each completion).
func (s *Store) TakeDiscoveryJob(ctx context.Context, proxyName string) (*DiscoveryJob, error) {
	s.expireStaleDiscoveryJobs(ctx)
	var running int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM discovery_jobs WHERE proxy_name=? AND state='dispatched'`, proxyName).Scan(&running); err != nil {
		return nil, err
	}
	if running > 0 {
		return nil, nil
	}
	var enc string
	row := s.db.QueryRowContext(ctx,
		`SELECT `+discoveryJobColumns+`, snmp_community FROM discovery_jobs
		 WHERE proxy_name=? AND state='pending' ORDER BY id LIMIT 1`, proxyName)
	var j DiscoveryJob
	err := row.Scan(&j.ID, &j.ProxyName, &j.Kind, &j.ControllerID, &j.ControllerName, &j.CIDR,
		&j.SNMPVersion, &j.SNMPPort, &j.State, &j.Error,
		&j.RequestedBy, &j.CreatedAt, &j.DispatchedAt, &j.CompletedAt, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE discovery_jobs SET state='dispatched', dispatched_at=? WHERE id=? AND state='pending'`,
		time.Now().Unix(), j.ID); err != nil {
		return nil, err
	}
	j.State = "dispatched"
	j.SNMPCommunity = s.cipher.Decrypt(enc)
	return &j, nil
}

// CompleteDiscoveryJob stores a scan's results and finishes the job. proxyName must own the job
// (the probe-token identity - ErrNotFound otherwise, without leaking whether the id exists). A job
// that already expired to failed still accepts late results; a done job doesn't. Results inherit
// the ignored state from the same IP's most recent prior result on this probe, so an ignored device
// stays ignored across re-scans. errMsg with zero hosts fails the job; with hosts it's kept as a
// note (e.g. a partial scan).
func (s *Store) CompleteDiscoveryJob(ctx context.Context, jobID int64, proxyName, errMsg string, results []DiscoveryResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, state string
	err = tx.QueryRowContext(ctx, `SELECT proxy_name, state FROM discovery_jobs WHERE id=?`, jobID).Scan(&owner, &state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (owner != proxyName || state == "done")) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	for _, r := range results {
		st := "new"
		var prev string
		err := tx.QueryRowContext(ctx,
			`SELECT dr.state FROM discovery_results dr JOIN discovery_jobs dj ON dr.job_id = dj.id
			 WHERE dj.proxy_name=? AND dr.ip=? ORDER BY dr.id DESC LIMIT 1`, proxyName, r.IP).Scan(&prev)
		if err == nil && prev == "ignored" {
			st = "ignored"
		}
		dns := 0
		if r.DNS {
			dns = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO discovery_results(job_id, ip, mac, rdns, tcp_ports, snmp_sysdescr, snmp_sysobjectid, snmp_sysname, http_json, dns, ssh_banner, unifi_json, suggested_class, state, host_id)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'')`,
			jobID, r.IP, r.MAC, r.RDNS, r.TCPPorts, r.SysDescr, r.SysObjectID, r.SysName, r.HTTPJSON,
			dns, r.SSHBanner, r.UniFiJSON, r.SuggestedClass, st); err != nil {
			return err
		}
	}
	final := "done"
	if errMsg != "" && len(results) == 0 {
		final = "failed"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE discovery_jobs SET state=?, error=?, completed_at=? WHERE id=?`,
		final, errMsg, time.Now().Unix(), jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListDiscoveryJobs returns the most recent scan jobs, newest first (community left empty), each
// with its result counts (found / still-new) for the history list.
func (s *Store) ListDiscoveryJobs(ctx context.Context, limit int) ([]DiscoveryJob, error) {
	s.expireStaleDiscoveryJobs(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+discoveryJobColumns+`,
		   (SELECT COUNT(*) FROM discovery_results r WHERE r.job_id = discovery_jobs.id),
		   (SELECT COUNT(*) FROM discovery_results r WHERE r.job_id = discovery_jobs.id AND r.state='new')
		 FROM discovery_jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryJob
	for rows.Next() {
		var j DiscoveryJob
		if err := rows.Scan(&j.ID, &j.ProxyName, &j.Kind, &j.ControllerID, &j.ControllerName, &j.CIDR,
			&j.SNMPVersion, &j.SNMPPort, &j.State, &j.Error,
			&j.RequestedBy, &j.CreatedAt, &j.DispatchedAt, &j.CompletedAt, &j.Found, &j.NewCount); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DiscoveryJobByID returns one scan job (community left empty), or ErrNotFound.
func (s *Store) DiscoveryJobByID(ctx context.Context, id int64) (*DiscoveryJob, error) {
	s.expireStaleDiscoveryJobs(ctx)
	j, err := scanDiscoveryJob(s.db.QueryRowContext(ctx,
		`SELECT `+discoveryJobColumns+` FROM discovery_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// DiscoveryResultsByJob returns a job's results in IP-insertion order (the probe sorts by IP).
func (s *Store) DiscoveryResultsByJob(ctx context.Context, jobID int64) ([]DiscoveryResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, ip, mac, rdns, tcp_ports, snmp_sysdescr, snmp_sysobjectid, snmp_sysname, http_json, dns, ssh_banner, unifi_json, suggested_class, state, host_id
		 FROM discovery_results WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryResult
	for rows.Next() {
		var r DiscoveryResult
		var dns int
		if err := rows.Scan(&r.ID, &r.JobID, &r.IP, &r.MAC, &r.RDNS, &r.TCPPorts, &r.SysDescr,
			&r.SysObjectID, &r.SysName, &r.HTTPJSON, &dns, &r.SSHBanner, &r.UniFiJSON, &r.SuggestedClass, &r.State, &r.HostID); err != nil {
			return nil, err
		}
		r.DNS = dns != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// DiscoveryResultByID returns one result (the adopt path resolves a sweep result's facts + job).
func (s *Store) DiscoveryResultByID(ctx context.Context, id int64) (*DiscoveryResult, error) {
	var r DiscoveryResult
	var dns int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, job_id, ip, mac, rdns, tcp_ports, snmp_sysdescr, snmp_sysobjectid, snmp_sysname, http_json, dns, ssh_banner, unifi_json, suggested_class, state, host_id
		 FROM discovery_results WHERE id=?`, id).
		Scan(&r.ID, &r.JobID, &r.IP, &r.MAC, &r.RDNS, &r.TCPPorts, &r.SysDescr,
			&r.SysObjectID, &r.SysName, &r.HTTPJSON, &dns, &r.SSHBanner, &r.UniFiJSON, &r.SuggestedClass, &r.State, &r.HostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.DNS = dns != 0
	return &r, nil
}

// SetDiscoveryResultsState flips results between new and ignored. Adopted rows are never touched.
func (s *Store) SetDiscoveryResultsState(ctx context.Context, ids []int64, state string) error {
	if state != "new" && state != "ignored" {
		return fmt.Errorf("invalid result state %q", state)
	}
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, state)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE discovery_results SET state=? WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`) AND state != 'added'`,
		args...)
	return err
}

// MarkDiscoveryResultAdded records that a result was adopted as the given Zabbix host.
func (s *Store) MarkDiscoveryResultAdded(ctx context.Context, id int64, hostID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE discovery_results SET state='added', host_id=? WHERE id=?`, hostID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
