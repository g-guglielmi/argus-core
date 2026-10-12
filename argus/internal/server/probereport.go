// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"time"

	"argus/internal/store"
)

// A probe's container reports to Argus every minute: its version, what it can do, its Zabbix process
// counts and CPU. That report is also what hands it discovery jobs (subnet scans, UniFi sweeps,
// certificate checks) and what the process sizing works from. Zabbix hearing from a probe says
// nothing about it (the proxy's data and the report are separate channels), and the updater sidecar
// checks in with the same token, so neither the "online" chip nor last_checkin shows a report that
// stopped. A probe Zabbix hears from whose report hasn't arrived for reportStaleAfter is flagged on
// the Probes page and in a system notice: probe images r8 to r28 sent one Argus couldn't read, and
// nothing showed it for two weeks.

const (
	reportStaleAfter = 5 * time.Minute  // a report is due every minute
	reportNoticeAge  = 15 * time.Minute // and the notice waits this long more, past a blip
)

// reportStale says whether a probe's minute report is missing. It can't be judged for a probe Zabbix
// doesn't hear from (that has its own alert) or one without a check-in credential (it never reports).
// The clock starts at the newest of its last report, its credential and this Argus's start, so a
// probe just enrolled, or a core just restarted, gets the whole grace.
func reportStale(ag store.ProbeAgent, zbxOnline bool, started, now int64) bool {
	if !zbxOnline || ag.CredentialAt == 0 {
		return false
	}
	ref := max(ag.ReportAt, ag.CredentialAt, started)
	return now-ref >= int64(reportStaleAfter/time.Second)
}

// reportStaleDetail words the notice: since when, and what's missing because of it.
func reportStaleDetail(ag store.ProbeAgent, loc *time.Location) string {
	d := "Zabbix hears from it, but its minute report has never reached Argus."
	if ag.ReportAt > 0 {
		d = "Zabbix still hears from it, but its minute report hasn't reached Argus since " +
			time.Unix(ag.ReportAt, 0).In(loc).Format("Jan 2, 15:04") + "."
	}
	return d + " Without it Argus can't hand it scans, UniFi sweeps or certificate checks, nor size its Zabbix processes." +
		" The probe's container log should say why; updating the probe may fix it."
}

// probeReportNotices: a condition per probe whose minute report is missing (one Zabbix call for the
// fleet's online state; nothing is flagged when that fails).
func (s *Server) probeReportNotices(ctx context.Context, agents map[string]store.ProbeAgent) []notice {
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		return nil
	}
	now := time.Now().Unix()
	var out []notice
	for _, p := range proxies {
		ag, ok := agents[p.Name]
		la := atoi64(p.LastAccess)
		if !ok || !reportStale(ag, la > 0 && now-la <= 120, s.startedAt.Unix(), now) {
			continue
		}
		site := probeSite(p.Name)
		out = append(out, notice{key: "probe-report:" + p.Name, title: "Probe " + site + " isn't reporting to Argus",
			detail: reportStaleDetail(ag, s.mgr.Location()), host: "Probe " + site, site: site, view: "probes", minAge: reportNoticeAge})
	}
	return out
}
