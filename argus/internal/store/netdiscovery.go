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

// ErrDiscoveryBusy is returned when a scan is queued for a probe that already has one in flight.
var ErrDiscoveryBusy = errors.New("a scan is already queued or running for this probe")

// Stale-job expiry: a pending job the probe never picked up (offline, or an image without the scan
// capability), and a dispatched job whose results never came back (the scanner's own budget is
// 8 minutes plus posting retries).
const (
	discoveryPendingMaxAge    = 5 * time.Minute
	discoveryDispatchedMaxAge = 15 * time.Minute
)

// DiscoveryJob is one queued network scan. SNMPCommunity is decrypted only by TakeDiscoveryJob
// (the check-in handout); listing reads leave it empty.
type DiscoveryJob struct {
	ID            int64
	ProxyName     string
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
	SuggestedClass string
	State          string // new | ignored | added
	HostID         string // Zabbix host id once adopted
}

const discoveryJobColumns = `id, proxy_name, cidr, snmp_version, snmp_port, state, error, requested_by, created_at, dispatched_at, completed_at`

func scanDiscoveryJob(row interface{ Scan(...any) error }) (DiscoveryJob, error) {
	var j DiscoveryJob
	err := row.Scan(&j.ID, &j.ProxyName, &j.CIDR, &j.SNMPVersion, &j.SNMPPort, &j.State, &j.Error,
		&j.RequestedBy, &j.CreatedAt, &j.DispatchedAt, &j.CompletedAt)
	return j, err
}

// expireStaleDiscoveryJobs fails jobs stuck in pending/dispatched past their grace periods, so a
// dead probe can never wedge its scan queue. Called lazily by every read/write below.
func (s *Store) expireStaleDiscoveryJobs(ctx context.Context) {
	now := time.Now()
	_, _ = s.db.ExecContext(ctx,
		`UPDATE discovery_jobs SET state='failed', error='the probe never picked the scan up - is it online and running a scan-capable image?', completed_at=?
		 WHERE state='pending' AND created_at < ?`,
		now.Unix(), now.Add(-discoveryPendingMaxAge).Unix())
	_, _ = s.db.ExecContext(ctx,
		`UPDATE discovery_jobs SET state='failed', error='the probe picked the scan up but never reported back', completed_at=?
		 WHERE state='dispatched' AND dispatched_at < ?`,
		now.Unix(), now.Add(-discoveryDispatchedMaxAge).Unix())
}

// CreateDiscoveryJob queues a scan (state pending). One scan in flight per probe: ErrDiscoveryBusy
// if a pending/dispatched job already exists. Older finished jobs beyond the last 10 per probe are
// pruned with their results.
func (s *Store) CreateDiscoveryJob(ctx context.Context, j DiscoveryJob) (int64, error) {
	s.expireStaleDiscoveryJobs(ctx)
	var active int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM discovery_jobs WHERE proxy_name=? AND state IN ('pending','dispatched')`,
		j.ProxyName).Scan(&active); err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, ErrDiscoveryBusy
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO discovery_jobs(proxy_name, cidr, snmp_version, snmp_community, snmp_port, state, error, requested_by, created_at, dispatched_at, completed_at)
		 VALUES(?,?,?,?,?,'pending','',?,?,0,0)`,
		j.ProxyName, j.CIDR, j.SNMPVersion, s.cipher.Encrypt(j.SNMPCommunity), j.SNMPPort,
		j.RequestedBy, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Prune: keep this probe's 10 most recent jobs.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM discovery_results WHERE job_id IN
		   (SELECT id FROM discovery_jobs WHERE proxy_name=? ORDER BY id DESC LIMIT -1 OFFSET 10)`, j.ProxyName)
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM discovery_jobs WHERE id IN
		   (SELECT id FROM discovery_jobs WHERE proxy_name=? ORDER BY id DESC LIMIT -1 OFFSET 10)`, j.ProxyName)
	return id, nil
}

// TakeDiscoveryJob hands a probe its oldest pending scan job (one-shot: pending -> dispatched) with
// the SNMP community decrypted for the handout. nil when nothing is queued.
func (s *Store) TakeDiscoveryJob(ctx context.Context, proxyName string) (*DiscoveryJob, error) {
	s.expireStaleDiscoveryJobs(ctx)
	var enc string
	row := s.db.QueryRowContext(ctx,
		`SELECT `+discoveryJobColumns+`, snmp_community FROM discovery_jobs
		 WHERE proxy_name=? AND state='pending' ORDER BY id LIMIT 1`, proxyName)
	var j DiscoveryJob
	err := row.Scan(&j.ID, &j.ProxyName, &j.CIDR, &j.SNMPVersion, &j.SNMPPort, &j.State, &j.Error,
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
			`INSERT INTO discovery_results(job_id, ip, mac, rdns, tcp_ports, snmp_sysdescr, snmp_sysobjectid, snmp_sysname, http_json, dns, suggested_class, state, host_id)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'')`,
			jobID, r.IP, r.MAC, r.RDNS, r.TCPPorts, r.SysDescr, r.SysObjectID, r.SysName, r.HTTPJSON,
			dns, r.SuggestedClass, st); err != nil {
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

// ListDiscoveryJobs returns the most recent scan jobs, newest first (community left empty).
func (s *Store) ListDiscoveryJobs(ctx context.Context, limit int) ([]DiscoveryJob, error) {
	s.expireStaleDiscoveryJobs(ctx)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+discoveryJobColumns+` FROM discovery_jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryJob
	for rows.Next() {
		j, err := scanDiscoveryJob(rows)
		if err != nil {
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
		`SELECT id, job_id, ip, mac, rdns, tcp_ports, snmp_sysdescr, snmp_sysobjectid, snmp_sysname, http_json, dns, suggested_class, state, host_id
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
			&r.SysObjectID, &r.SysName, &r.HTTPJSON, &dns, &r.SuggestedClass, &r.State, &r.HostID); err != nil {
			return nil, err
		}
		r.DNS = dns != 0
		out = append(out, r)
	}
	return out, rows.Err()
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
