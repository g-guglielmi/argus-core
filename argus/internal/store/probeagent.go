// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ProbeAgent is a probe's fleet-update state: the version it last reported, whether its
// self-updater is enabled, and when it last checked in. Keyed by proxy name.
type ProbeAgent struct {
	ProxyName      string
	Version        string
	SelfUpdate     bool
	Scans          bool   // the probe container advertises the network-scan capability (§B discovery)
	Sweeps         bool   // the probe container advertises the UniFi-sweep capability (§B discovery)
	LastCheckin    int64  // unix seconds, 0 if never (the proxy container's check-ins or its sidecar's)
	UpdaterVersion string // version of the argus-updater sidecar managing this probe, "" if none
	// ReportAt is when the proxy container's own minute report last arrived (0 = never): only it
	// carries the scan capability, so the sidecar's check-ins and the start-time sync don't move it.
	// CredentialAt is when the probe got its check-in credential (enrollment, or Enable reporting).
	ReportAt     int64
	CredentialAt int64
	// Break-glass console credential (VM probes). The secret itself is never carried here - only
	// whether one exists + its username + when it was last reported; reveal it via BreakGlass().
	BreakGlassUser string
	BreakGlassSet  bool
	BreakGlassAt   int64
	// OS patch status a VM probe's host-side reporter posts (DESIGN §14c). SecUpdates is the pending
	// security-update count, or -1 when never reported; RebootRequired mirrors /var/run/reboot-required.
	SecUpdates     int
	RebootRequired bool
	OSReportedAt   int64
	OSVersion      string
	// HostTools is the sidecar's last report on the VM's host scripts and units (JSON: state,
	// message, version, at, installed, apply), "" until one arrived (DESIGN 14c).
	HostTools string
	// CAFingerprint is the SHA-256 (hex) of the monitoring CA the probe's ca.crt holds, as its minute
	// report last gave it; "" until a probe image that reports it checked in.
	CAFingerprint string
	// CertEnd is when the probe's own certificate ends (unix seconds, 0 = not reported);
	// CertRenewedAt when Argus last signed it a new one.
	CertEnd       int64
	CertRenewedAt int64
	// One-shot updates queued for the next check-in of the probe's sidecar ("" when none): the
	// proxy's and the sidecar's own.
	PendingUpdate        string
	PendingUpdaterUpdate string
	// Zabbix process counts Argus manages for this probe (probeprocs.go).
	Procs ProbeProcs
}

// UpsertProbeCredential issues (or rotates) a probe's long-lived check-in credential at
// enrollment. A re-enrollment resets the reported state so stale version data doesn't linger.
func (s *Store) UpsertProbeCredential(ctx context.Context, proxyName, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO probe_agents(proxy_name,token_hash,created_at,version,selfupdate,last_checkin)
		 VALUES(?,?,?,'',0,0)
		 ON CONFLICT(proxy_name) DO UPDATE SET token_hash=excluded.token_hash, created_at=excluded.created_at,
		   version='', selfupdate=0, last_checkin=0, report_at=0, ca_fp='', cert_end=0`,
		proxyName, tokenHash, time.Now().Unix())
	return err
}

// ProbeNameByToken returns the proxy name for a valid check-in credential (by token hash).
func (s *Store) ProbeNameByToken(ctx context.Context, tokenHash string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT proxy_name FROM probe_agents WHERE token_hash=?`, tokenHash).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// RecordProbeCheckin refreshes a probe's reported version, self-updater flag, network-scan
// capability, and check-in time. All fields are "sticky when omitted", because a probe can be
// reported by two check-ins with complementary knowledge: the proxy container reports its real
// version and scan capability but (when it holds no socket) omits self-update capability, while a
// socket-holding updater sidecar advertises capability but reports no version. An empty version
// keeps the last known version; a nil selfUpdate/scans keeps the last known flag - so the two never
// clobber each other. last_checkin is always refreshed; report_at only by the proxy container's
// minute report, the one check-in that carries the scan capability.
func (s *Store) RecordProbeCheckin(ctx context.Context, proxyName, version string, selfUpdate, scans, sweeps *bool) error {
	now := time.Now().Unix()
	set := "last_checkin=?"
	args := []any{now}
	if scans != nil {
		set += ", report_at=?"
		args = append(args, now)
	}
	if strings.TrimSpace(version) != "" {
		set += ", version=?"
		args = append(args, version)
	}
	if selfUpdate != nil {
		su := 0
		if *selfUpdate {
			su = 1
		}
		set += ", selfupdate=?"
		args = append(args, su)
	}
	if scans != nil {
		sc := 0
		if *scans {
			sc = 1
		}
		set += ", scans=?"
		args = append(args, sc)
	}
	if sweeps != nil {
		sw := 0
		if *sweeps {
			sw = 1
		}
		set += ", sweeps=?"
		args = append(args, sw)
	}
	args = append(args, proxyName)
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET `+set+` WHERE proxy_name=?`, args...)
	return err
}

// ProbeAgentByName returns one probe's fleet-update state, or ErrNotFound if it never enrolled
// through Argus (no check-in credential).
func (s *Store) ProbeAgentByName(ctx context.Context, name string) (*ProbeAgent, error) {
	var a ProbeAgent
	var su, sc, sw, bg, rr int
	var pr probeProcsScan
	err := s.db.QueryRowContext(ctx,
		`SELECT proxy_name,version,selfupdate,scans,sweeps,last_checkin,report_at,created_at,updater_version,bg_user,(bg_secret != ''),bg_updated_at,sec_updates,reboot_required,os_reported_at,os_version,update_to,updater_update_to,host_tools,ca_fp,cert_end,cert_renewed_at,`+probeProcsColumns+`
		 FROM probe_agents WHERE proxy_name=?`, name).
		Scan(append([]any{&a.ProxyName, &a.Version, &su, &sc, &sw, &a.LastCheckin, &a.ReportAt, &a.CredentialAt, &a.UpdaterVersion, &a.BreakGlassUser, &bg, &a.BreakGlassAt, &a.SecUpdates, &rr, &a.OSReportedAt, &a.OSVersion, &a.PendingUpdate, &a.PendingUpdaterUpdate, &a.HostTools, &a.CAFingerprint, &a.CertEnd, &a.CertRenewedAt}, pr.dest(&a.Procs)...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pr.decode(&a.Procs)
	a.SelfUpdate = su != 0
	a.Scans = sc != 0
	a.Sweeps = sw != 0
	a.BreakGlassSet = bg != 0
	a.RebootRequired = rr != 0
	return &a, nil
}

// SetProbeCA records the fingerprint of the CA a probe trusts. Empty is ignored (a check-in that
// doesn't carry it keeps the last one).
func (s *Store) SetProbeCA(ctx context.Context, proxyName, fp string) error {
	if fp == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET ca_fp=? WHERE proxy_name=?`, fp, proxyName)
	return err
}

// SetProbeCertEnd records when a probe's certificate ends, as its report gives it (0 is ignored).
func (s *Store) SetProbeCertEnd(ctx context.Context, proxyName string, end int64) error {
	if end <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET cert_end=? WHERE proxy_name=?`, end, proxyName)
	return err
}

// MarkProbeCertRenewed records a certificate Argus signed for a probe: its end, and now.
func (s *Store) MarkProbeCertRenewed(ctx context.Context, proxyName string, end int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET cert_end=?, cert_renewed_at=? WHERE proxy_name=?`, end, time.Now().Unix(), proxyName)
	return err
}

// SetProbeHostTools records the sidecar's report on the probe VM's host tools (a JSON document the
// handler already cleaned). Empty leaves the stored one alone; ErrNotFound for an unknown probe.
func (s *Store) SetProbeHostTools(ctx context.Context, proxyName, doc string) error {
	if strings.TrimSpace(doc) == "" {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET host_tools=? WHERE proxy_name=?`, doc, proxyName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProbeUpdate queues a self-update for a probe: the target image tag it should converge on,
// handed to it once at its next check-in. Returns ErrNotFound if the probe isn't known.
func (s *Store) SetProbeUpdate(ctx context.Context, name, tag string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET update_to=? WHERE proxy_name=?`, tag, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TakeProbeUpdate returns and clears any pending self-update tag for a probe (one-shot). An empty
// string means nothing is queued.
func (s *Store) TakeProbeUpdate(ctx context.Context, name string) (string, error) {
	var tag string
	err := s.db.QueryRowContext(ctx, `SELECT update_to FROM probe_agents WHERE proxy_name=?`, name).Scan(&tag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if tag != "" {
		// Clear exactly what was read: if another check-in took it meanwhile, this one gets nothing.
		res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET update_to='' WHERE proxy_name=? AND update_to=?`, name, tag)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", nil
		}
	}
	return tag, nil
}

// ProbeAgents returns every probe's fleet-update state, keyed by proxy name (for the fleet view).
func (s *Store) ProbeAgents(ctx context.Context) (map[string]ProbeAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name,version,selfupdate,scans,sweeps,last_checkin,report_at,created_at,updater_version,bg_user,(bg_secret != ''),bg_updated_at,sec_updates,reboot_required,os_reported_at,os_version,update_to,updater_update_to,host_tools,ca_fp,cert_end,cert_renewed_at,`+probeProcsColumns+` FROM probe_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ProbeAgent)
	for rows.Next() {
		var a ProbeAgent
		var su, sc, sw, bg, rr int
		var pr probeProcsScan
		if err := rows.Scan(append([]any{&a.ProxyName, &a.Version, &su, &sc, &sw, &a.LastCheckin, &a.ReportAt, &a.CredentialAt, &a.UpdaterVersion, &a.BreakGlassUser, &bg, &a.BreakGlassAt, &a.SecUpdates, &rr, &a.OSReportedAt, &a.OSVersion, &a.PendingUpdate, &a.PendingUpdaterUpdate, &a.HostTools, &a.CAFingerprint, &a.CertEnd, &a.CertRenewedAt}, pr.dest(&a.Procs)...)...); err != nil {
			return nil, err
		}
		pr.decode(&a.Procs)
		a.SelfUpdate = su != 0
		a.Scans = sc != 0
		a.Sweeps = sw != 0
		a.BreakGlassSet = bg != 0
		a.RebootRequired = rr != 0
		out[a.ProxyName] = a
	}
	return out, rows.Err()
}

// SetProbeOSStatus records a probe VM's OS patch status - the pending security-update count, whether
// the OS needs a reboot, and the OS pretty-name - reported by the VM's host-side timer (DESIGN §14c).
// A negative count is clamped to -1 (unknown). ErrNotFound if the probe never enrolled through Argus.
func (s *Store) SetProbeOSStatus(ctx context.Context, proxyName string, secUpdates int, rebootRequired bool, osVersion string) error {
	if secUpdates < 0 {
		secUpdates = -1
	}
	rr := 0
	if rebootRequired {
		rr = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE probe_agents SET sec_updates=?, reboot_required=?, os_reported_at=?, os_version=? WHERE proxy_name=?`,
		secUpdates, rr, time.Now().Unix(), strings.TrimSpace(osVersion), proxyName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBreakGlass stores (or rotates) a probe's break-glass console credential - the per-VM admin
// username + password a probe VM generates and reports at first boot. The password is encrypted at
// rest with the existing cipher. ErrNotFound if the probe never enrolled through Argus.
func (s *Store) SetBreakGlass(ctx context.Context, proxyName, user, secretPlain string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE probe_agents SET bg_user=?, bg_secret=?, bg_updated_at=? WHERE proxy_name=?`,
		user, s.cipher.Encrypt(secretPlain), time.Now().Unix(), proxyName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BreakGlass returns a probe's decrypted break-glass credential for an admin reveal. The password is
// empty if none has been reported. ErrNotFound if the probe isn't known to Argus.
func (s *Store) BreakGlass(ctx context.Context, proxyName string) (user, secret string, updatedAt int64, err error) {
	var enc string
	err = s.db.QueryRowContext(ctx,
		`SELECT bg_user,bg_secret,bg_updated_at FROM probe_agents WHERE proxy_name=?`, proxyName).
		Scan(&user, &enc, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrNotFound
	}
	if err != nil {
		return "", "", 0, err
	}
	return user, s.cipher.Decrypt(enc), updatedAt, nil
}

// SetUpdaterVersion records the version reported by the argus-updater sidecar managing this probe.
// Empty is ignored (sticky), so a check-in that omits it never erases the last known value.
func (s *Store) SetUpdaterVersion(ctx context.Context, name, version string) error {
	if strings.TrimSpace(version) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET updater_version=? WHERE proxy_name=?`, version, name)
	return err
}

// SetUpdaterUpdate queues a self-update for a probe's updater sidecar: the argus-updater image tag it
// should recreate ITSELF onto, handed to it once at its next check-in. ErrNotFound if unknown.
func (s *Store) SetUpdaterUpdate(ctx context.Context, name, tag string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET updater_update_to=? WHERE proxy_name=?`, tag, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TakeUpdaterUpdate returns and clears any pending updater self-update tag (one-shot). Empty means
// nothing queued.
func (s *Store) TakeUpdaterUpdate(ctx context.Context, name string) (string, error) {
	var tag string
	err := s.db.QueryRowContext(ctx, `SELECT updater_update_to FROM probe_agents WHERE proxy_name=?`, name).Scan(&tag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if tag != "" {
		res, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET updater_update_to='' WHERE proxy_name=? AND updater_update_to=?`, name, tag)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", nil
		}
	}
	return tag, nil
}

// --- probe fleet target version (app_meta) ---

const probeTargetKey = "probe_target_version"

// ProbeTargetVersion returns the fleet's target probe version ("latest" by default).
func (s *Store) ProbeTargetVersion(ctx context.Context) (string, error) {
	v, ok, err := s.MetaGet(ctx, probeTargetKey)
	if err != nil {
		return "", err
	}
	if !ok || v == "" {
		return "latest", nil
	}
	return v, nil
}

// SetProbeTargetVersion stores the fleet's target probe version.
func (s *Store) SetProbeTargetVersion(ctx context.Context, v string) error {
	return s.MetaSet(ctx, probeTargetKey, v)
}
