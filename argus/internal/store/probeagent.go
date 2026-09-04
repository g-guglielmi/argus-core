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
	LastCheckin    int64  // unix seconds, 0 if never
	UpdaterVersion string // version of the argus-updater sidecar managing this probe, "" if none
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
}

// UpsertProbeCredential issues (or rotates) a probe's long-lived check-in credential at
// enrollment. A re-enrollment resets the reported state so stale version data doesn't linger.
func (s *Store) UpsertProbeCredential(ctx context.Context, proxyName, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO probe_agents(proxy_name,token_hash,created_at,version,selfupdate,last_checkin)
		 VALUES(?,?,?,'',0,0)
		 ON CONFLICT(proxy_name) DO UPDATE SET token_hash=excluded.token_hash, created_at=excluded.created_at,
		   version='', selfupdate=0, last_checkin=0`,
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

// RecordProbeCheckin refreshes a probe's reported version, self-updater flag, and check-in time.
// Both fields are "sticky when omitted", because a probe can be reported by two check-ins with
// complementary knowledge: the proxy container reports its real version but (when it holds no socket)
// omits self-update capability, while a socket-holding updater sidecar advertises capability but
// reports no version. An empty version keeps the last known version; a nil selfUpdate keeps the last
// known flag - so the two never clobber each other. last_checkin is always refreshed.
func (s *Store) RecordProbeCheckin(ctx context.Context, proxyName, version string, selfUpdate *bool) error {
	set := "last_checkin=?"
	args := []any{time.Now().Unix()}
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
	args = append(args, proxyName)
	_, err := s.db.ExecContext(ctx, `UPDATE probe_agents SET `+set+` WHERE proxy_name=?`, args...)
	return err
}

// ProbeAgentByName returns one probe's fleet-update state, or ErrNotFound if it never enrolled
// through Argus (no check-in credential).
func (s *Store) ProbeAgentByName(ctx context.Context, name string) (*ProbeAgent, error) {
	var a ProbeAgent
	var su, bg, rr int
	err := s.db.QueryRowContext(ctx,
		`SELECT proxy_name,version,selfupdate,last_checkin,updater_version,bg_user,(bg_secret != ''),bg_updated_at,sec_updates,reboot_required,os_reported_at
		 FROM probe_agents WHERE proxy_name=?`, name).
		Scan(&a.ProxyName, &a.Version, &su, &a.LastCheckin, &a.UpdaterVersion, &a.BreakGlassUser, &bg, &a.BreakGlassAt, &a.SecUpdates, &rr, &a.OSReportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.SelfUpdate = su != 0
	a.BreakGlassSet = bg != 0
	a.RebootRequired = rr != 0
	return &a, nil
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
		_, _ = s.db.ExecContext(ctx, `UPDATE probe_agents SET update_to='' WHERE proxy_name=?`, name)
	}
	return tag, nil
}

// ProbeAgents returns every probe's fleet-update state, keyed by proxy name (for the fleet view).
func (s *Store) ProbeAgents(ctx context.Context) (map[string]ProbeAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_name,version,selfupdate,last_checkin,updater_version,bg_user,(bg_secret != ''),bg_updated_at,sec_updates,reboot_required,os_reported_at FROM probe_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ProbeAgent)
	for rows.Next() {
		var a ProbeAgent
		var su, bg, rr int
		if err := rows.Scan(&a.ProxyName, &a.Version, &su, &a.LastCheckin, &a.UpdaterVersion, &a.BreakGlassUser, &bg, &a.BreakGlassAt, &a.SecUpdates, &rr, &a.OSReportedAt); err != nil {
			return nil, err
		}
		a.SelfUpdate = su != 0
		a.BreakGlassSet = bg != 0
		a.RebootRequired = rr != 0
		out[a.ProxyName] = a
	}
	return out, rows.Err()
}

// SetProbeOSStatus records a probe VM's OS patch status - the pending security-update count and
// whether the OS needs a reboot - reported by the VM's host-side timer (DESIGN §14c). A negative
// count is clamped to -1 (unknown). ErrNotFound if the probe never enrolled through Argus.
func (s *Store) SetProbeOSStatus(ctx context.Context, proxyName string, secUpdates int, rebootRequired bool) error {
	if secUpdates < 0 {
		secUpdates = -1
	}
	rr := 0
	if rebootRequired {
		rr = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE probe_agents SET sec_updates=?, reboot_required=?, os_reported_at=? WHERE proxy_name=?`,
		secUpdates, rr, time.Now().Unix(), proxyName)
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
		_, _ = s.db.ExecContext(ctx, `UPDATE probe_agents SET updater_update_to='' WHERE proxy_name=?`, name)
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
