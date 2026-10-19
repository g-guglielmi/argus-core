// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"

	"argus/internal/provision"
)

// Core health (DESIGN 18d): the Zabbix server's own host, "Zabbix server", which Zabbix creates
// itself, is adopted by Argus at startup as the Argus core: it takes the core class and the Argus
// Core Health template, whose internal checks the server runs on itself, so its sensors are the
// capacity gauges the sizing pass (14b) is built on: required performance and values per second,
// the caches, the processes, hosts, items, unsupported and delayed items. Zabbix's stock server
// health template reads the same keys with its own thresholds, so it makes way (unlinked and
// cleared); whatever agent template the host has (Linux by Zabbix agent on the appliance) stays and
// keeps reading the VM's CPU, memory and disks. A core installed by hand whose server host was
// renamed is left alone and logged.

// coreHostName is the technical name of the Zabbix server's own host.
const coreHostName = "Zabbix server"

// stockServerHealth is Zabbix's own server health template, replaced on that host by Argus Core Health.
const stockServerHealth = "Zabbix server health"

// corePlan is what adopting the core host takes, given the templates linked to it now: nothing when
// Argus Core Health is already there; otherwise linking it, after unlinking the stock template when
// that is linked (the two read the same keys, and Zabbix refuses a second item with a key a host has).
type corePlan struct {
	unlinkStock bool
	link        bool
}

func coreHostPlan(linked []string) corePlan {
	var p corePlan
	hasCore := false
	for _, t := range linked {
		switch t {
		case provision.TemplateCoreHealth:
			hasCore = true
		case stockServerHealth:
			p.unlinkStock = true
		}
	}
	if hasCore {
		return corePlan{}
	}
	p.link = true
	return p
}

// EnsureCoreHost adopts the server's own host (idempotent; runs after the template import at
// startup). Best-effort: a failure is logged and tried again at the next start.
func (s *Server) EnsureCoreHost(ctx context.Context) {
	if !s.zbx.Authenticated() {
		return
	}
	hostID, err := s.zbx.HostIDByName(ctx, coreHostName)
	if err != nil {
		s.logger.Warn("core host: could not look up the Zabbix server host", "err", err)
		return
	}
	if hostID == "" {
		s.logger.Info("core host: no host named " + coreHostName + ": the core's own health is not monitored (rename the server's host back to adopt it)")
		return
	}
	classes, _ := s.st.DeviceClasses(ctx)
	linked, err := s.zbx.HostLinkedTemplateNames(ctx, hostID)
	if err != nil {
		s.logger.Warn("core host: could not read the Zabbix server host's templates", "err", err)
		return
	}
	plan := coreHostPlan(linked)
	if !plan.link && classes[hostID] == provision.ClassCore {
		return // adopted already
	}
	if plan.link {
		tids, err := s.zbx.TemplateIDsByName(ctx, []string{provision.TemplateCoreHealth, stockServerHealth})
		if err != nil {
			s.logger.Warn("core host: could not look up the templates", "err", err)
			return
		}
		if plan.unlinkStock {
			if id := tids[stockServerHealth]; id != "" {
				if err := s.zbx.UnlinkHostTemplate(ctx, hostID, id); err != nil {
					s.logger.Warn("core host: could not unlink the stock server health template", "err", err)
					return
				}
			}
		}
		if err := s.zbx.LinkHostTemplate(ctx, hostID, tids[provision.TemplateCoreHealth]); err != nil {
			s.logger.Warn("core host: could not link Argus Core Health", "err", err)
			return
		}
	}
	if err := s.zbx.SetHostTag(ctx, hostID, "argus.class", provision.ClassCore); err != nil {
		s.logger.Warn("core host: could not set the argus.class tag", "err", err)
	}
	if err := s.st.SetDeviceClass(ctx, hostID, provision.ClassCore, "auto"); err != nil {
		s.logger.Warn("core host: could not record the class overlay", "err", err)
		return
	}
	s.logger.Info("core host: adopted the Zabbix server host as the Argus core", "host", hostID, "replaced_stock_template", plan.unlinkStock)
}
