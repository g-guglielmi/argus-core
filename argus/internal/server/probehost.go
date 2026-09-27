// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"strings"

	"argus/internal/provision"
	"argus/internal/zabbix"
)

// Proxy health (Settings expansion, §D): every probe gets one Argus-managed "Probe <site>" host in
// its site group, monitored BY that proxy and with no interface, carrying the Argus Probe Health
// template. Zabbix runs internal checks on the proxy that monitors the host, so its sensors measure
// the proxy itself (unsent values, queue, caches, busy processes), and a strict nodata() trigger on
// its uptime raises "probe unreachable" while the proxy is offline. It reuses the tree, charts,
// thresholds and notifications like any other host.

// probeSite is the site a proxy serves ("proxy-site1" -> "site1"), which is also its host group.
func probeSite(proxyName string) string { return strings.TrimPrefix(proxyName, "proxy-") }

// probeHostName is the technical Zabbix name of a proxy's Probe host.
func probeHostName(proxyName string) string { return "argus-probe-" + probeSite(proxyName) }

// probeHostTag names the tag that ties a Probe host to its proxy.
const probeHostTag = "argus.probe"

// ensureProbeHost creates the proxy's Probe host if it doesn't exist yet. It never creates the site
// group: if the operator deleted it (or the proxy doesn't follow the proxy-<site> naming), the proxy
// is skipped and reported, rather than a group being brought back.
func (s *Server) ensureProbeHost(ctx context.Context, p zabbix.Proxy) (created bool, err error) {
	name := probeHostName(p.Name)
	if id, err := s.zbx.HostIDByName(ctx, name); err != nil || id != "" {
		return false, err
	}
	site := probeSite(p.Name)
	gid, err := s.zbx.HostGroupIDByName(ctx, site)
	if err != nil {
		return false, err
	}
	if gid == "" {
		return false, fmt.Errorf("no host group %q for probe %q; create it (or rename the proxy proxy-<site>) and restart Argus", site, p.Name)
	}
	tids, err := s.zbx.TemplateIDsByName(ctx, []string{provision.TemplateProbeHealth})
	if err != nil {
		return false, err
	}
	hostID, err := s.zbx.CreateHost(ctx, zabbix.CreateHostParams{
		Host:        name,
		Name:        "Probe " + site,
		GroupIDs:    []string{gid},
		TemplateIDs: []string{tids[provision.TemplateProbeHealth]},
		MonitoredBy: 1,
		ProxyID:     p.ProxyID,
		Tags: []zabbix.HostTag{
			{Tag: "argus.class", Value: provision.ClassProbe},
			{Tag: "argus.source", Value: "auto"},
			{Tag: probeHostTag, Value: p.Name},
		},
		Description: "Health of the " + p.Name + " probe, measured by the probe itself. Created and managed by Argus.",
	})
	if err != nil {
		return false, err
	}
	if err := s.st.SetDeviceClass(ctx, hostID, provision.ClassProbe, "auto"); err != nil {
		return true, err
	}
	return true, nil
}

// EnsureProbeHosts gives every proxy its Probe host (idempotent; runs after the template import at
// startup, and after each enrollment). Best-effort: a failure on one proxy doesn't stop the others.
func (s *Server) EnsureProbeHosts(ctx context.Context) {
	if !s.zbx.Authenticated() {
		return
	}
	proxies, err := s.zbx.Proxies(ctx)
	if err != nil {
		s.logger.Warn("probe hosts: could not list proxies", "err", err)
		return
	}
	for _, p := range proxies {
		created, err := s.ensureProbeHost(ctx, p)
		if err != nil {
			s.logger.Warn("probe hosts: could not ensure the Probe host", "proxy", p.Name, "err", err)
			continue
		}
		if created {
			s.logger.Info("probe hosts: created the Probe host", "proxy", p.Name, "host", probeHostName(p.Name))
		}
	}
}

// deleteProbeHost removes a proxy's Probe host (before the proxy itself: Zabbix refuses to delete a
// proxy that still monitors hosts). A missing host is not an error.
func (s *Server) deleteProbeHost(ctx context.Context, proxyName string) error {
	id, err := s.zbx.HostIDByName(ctx, probeHostName(proxyName))
	if err != nil || id == "" {
		return err
	}
	if err := s.zbx.DeleteHost(ctx, id); err != nil {
		return err
	}
	if err := s.st.DeleteDeviceClass(ctx, id); err != nil {
		s.logger.Warn("probe hosts: could not drop the class overlay", "host", id, "err", err)
	}
	s.logger.Info("probe hosts: deleted the Probe host", "proxy", proxyName)
	return nil
}
