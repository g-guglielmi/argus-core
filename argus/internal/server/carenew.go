// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// The monitoring CA's renewal (DESIGN 14c). The core VM's argus-cert-renew signs the CA certificate
// again with its own key two years before its end: the same name and key, a later end, so every
// certificate it signed stays trusted. Argus reads the renewed file without a restart, hands it to
// each probe whose minute report names another CA (the probe takes only a renewal of the CA it has),
// and says what is left to people: the PCs that trust the old certificate, a probe that doesn't take
// the renewed one, and a CA close to its end that nobody renewed (Argus on its own machine).

const (
	caRefreshEvery = 5 * time.Minute
	// caSeenKey is the CA Argus last saw ("fingerprint|end unix"); caRenewedKey the last renewal it
	// saw ("fingerprint|old end unix"): the probes still on the old CA keep that end until they move.
	caSeenKey    = "ca_seen"
	caRenewedKey = "ca_renewed"
	// caEndingDays: a CA this close to its end that is still not renewed is told (the core VM renews
	// it 730 days ahead, so on the appliance this means the renewal didn't happen).
	caEndingDays = 365
	// caProbeGrace: a probe hands its report every minute and takes the renewed CA at once; one still
	// on another CA after this long refused it or can't take it.
	caProbeGrace = 24 * time.Hour
)

// startCARefresh reads the CA certificate again every few minutes, so a renewal on the core VM
// reaches the enrollments and the probes without a restart of Argus.
func (s *Server) startCARefresh(ctx context.Context) {
	if s.ca == nil {
		return
	}
	go func() {
		t := time.NewTicker(caRefreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if changed, err := s.ca.Refresh(); err != nil {
				s.logger.Warn("monitoring CA not reloaded", "err", err)
			} else if changed {
				s.logger.Info("monitoring CA renewed: probes take it at their next check-in", "ends", s.ca.NotAfter().UTC().Format("2006-01-02"), "sha256", s.ca.Fingerprint())
			}
		}
	}()
}

// caNotices: the renewal itself (an event), a CA close to its end, and the probes that don't take the
// renewed one (conditions).
func (s *Server) caNotices(ctx context.Context, now time.Time) (conds, events []notice) {
	if s.ca == nil {
		return nil, nil
	}
	agents, _ := s.st.ProbeAgents(ctx)
	fp, end := s.ca.Fingerprint(), s.ca.NotAfter()
	day := func(t time.Time) string { return t.In(s.mgr.Location()).Format("Jan 2, 2006") }

	if seenFP, seenEnd, ok := s.metaCA(ctx, caSeenKey); !ok || seenFP != fp {
		if ok && end.After(seenEnd) {
			_ = s.st.MetaSet(ctx, caRenewedKey, fp+"|"+itoa64(seenEnd.Unix()))
			events = append(events, notice{key: "ca-renewed:" + fp, title: "The monitoring CA was renewed",
				detail: "It now ends on " + day(end) + " (same name and key, so every certificate it signed stays trusted). " +
					"Probes take it at their next check-in. PCs that trust the old certificate keep working until " + day(seenEnd) +
					": install the renewed ca.crt (/docker/argus/pki/ca.crt on the core) on them before then.",
				view: "probes"})
		}
		_ = s.st.MetaSet(ctx, caSeenKey, fp+"|"+itoa64(end.Unix()))
	}

	if left := end.Sub(now); left < caEndingDays*24*time.Hour {
		conds = append(conds, notice{key: "ca-ending:" + fp, title: "The monitoring CA ends on " + day(end),
			detail: "Every probe and the Zabbix server trust it, and nothing renewed it (the core VM does so two years ahead). " +
				"Sign it again with its own key and name, as the deploy README says, and Argus hands it to the probes.",
			view: "probes"})
	}

	renewedFP, oldEnd, renewed := s.metaCA(ctx, caRenewedKey)
	renewed = renewed && renewedFP == fp
	for name, ag := range agents {
		// Only probes reporting now: one that is down has notices of its own.
		if ag.ReportAt == 0 || now.Sub(time.Unix(ag.ReportAt, 0)) > 2*time.Hour {
			continue
		}
		site := probeSite(name)
		ends := ""
		if renewed {
			ends = " It loses the core on " + day(oldEnd) + ", when the old one ends."
		}
		switch {
		case ag.CAFingerprint != "" && ag.CAFingerprint != fp:
			conds = append(conds, notice{key: "probe-ca:" + name + ":" + fp, title: "Probe " + site + " doesn't take the monitoring CA",
				detail: "Argus hands it the CA at every check-in and its ca.crt still holds another one; the probe's log says why (argus-probe: ...)." + ends,
				host:   "Probe " + site, site: site, view: "probes", minAge: caProbeGrace, cleared: "Probe " + site + " took the monitoring CA"})
		case ag.CAFingerprint == "" && renewed:
			conds = append(conds, notice{key: "probe-ca:" + name + ":" + fp, title: "Probe " + site + " can't take the renewed monitoring CA",
				detail: "Its image is older than the CA renewal: update it, and it takes the CA by itself." + ends,
				host:   "Probe " + site, site: site, view: "updates", minAge: caProbeGrace, cleared: "Probe " + site + " took the monitoring CA"})
		}
	}
	return conds, events
}

// metaCA reads a "fingerprint|unix" pair from app_meta.
func (s *Server) metaCA(ctx context.Context, key string) (string, time.Time, bool) {
	raw, ok, _ := s.st.MetaGet(ctx, key)
	fp, at, found := strings.Cut(raw, "|")
	if !ok || !found {
		return "", time.Time{}, false
	}
	n, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return fp, time.Unix(n, 0), true
}
