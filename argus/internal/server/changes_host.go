// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"argus/internal/store"
)

// proxyNames maps proxy ids to names ("0" = the server), for the change log. Best-effort.
func (s *Server) proxyNames(ctx context.Context) map[string]string {
	out := map[string]string{"0": "Server", "": "Server"}
	if ps, err := s.zbx.Proxies(ctx); err == nil {
		for _, p := range ps {
			out[p.ProxyID] = p.Name
		}
	}
	return out
}

// hostConfigDiff is what a host settings save changed, as the change log shows it. Secrets read
// "changed", never their values.
func hostConfigDiff(before hostConfigView, req hostConfigUpdate, proxies map[string]string, hostNames ...map[string]string) []store.ChangeDiff {
	var out []store.ChangeDiff
	add := func(f, o, n string) {
		if o != n {
			out = append(out, store.ChangeDiff{Field: f, Old: o, New: n})
		}
	}
	if req.Name != "" {
		add("Visible name", before.Name, req.Name)
	}
	add("Technical name", before.Host, req.Host)
	monitored := func(by int, proxy string) string {
		if by == 0 {
			return "Server"
		}
		if n := proxies[proxy]; n != "" {
			return n
		}
		return "probe " + proxy
	}
	add("Monitored by", monitored(before.MonitoredBy, before.ProxyID), monitored(req.MonitoredBy, strings.TrimSpace(req.ProxyID)))
	add("Interfaces", ifaceSummary(before.Interfaces), ifaceSummary(req.Interfaces))

	if req.Macros != nil {
		for _, f := range before.Macros {
			nv, ok := req.Macros[f.Macro]
			if !ok {
				continue
			}
			nv = strings.TrimSpace(nv)
			if f.Secret {
				if nv != "" {
					out = append(out, store.ChangeDiff{Field: f.Label, Old: "", New: "changed"})
				}
				continue
			}
			add(f.Label, orDefault(f.Value, ""), orDefault(nv, ""))
		}
		for _, t := range before.Thresholds {
			nv, ok := req.Macros[t.Macro]
			if !ok {
				continue
			}
			nv = strings.TrimSpace(nv)
			add(t.Label, thresholdText(t.Value, t.Default, t.Unit), thresholdText(nv, t.Default, t.Unit))
		}
	}
	if req.AddOns != nil {
		for _, a := range before.AddOns {
			d, ok := req.AddOns[a.ID]
			if !ok {
				continue
			}
			add(a.Label, onOff(a.Enabled), onOff(d.Enabled))
			if !d.Enabled {
				continue
			}
			for _, m := range a.Macros {
				if nv, ok := d.Macros[m.Macro]; ok {
					add(a.Label+" · "+m.Label, m.Value, strings.TrimSpace(nv))
				}
			}
		}
	}
	if req.Master != nil && before.Master != nil {
		label := func(id string) string {
			switch id {
			case "", "none":
				return "none"
			case "default":
				id = before.Master.DefaultID
			}
			for _, o := range before.Master.Options {
				if o.ID == id {
					return o.Label
				}
			}
			return id
		}
		old := before.Master.ItemID
		if !before.Master.Custom {
			old = "default"
		}
		if old != *req.Master {
			add("Master sensor", label(old), label(*req.Master))
		}
	}
	if req.Upstream != nil {
		say := func(mode, host string) string {
			switch mode {
			case "manual":
				if len(hostNames) > 0 && hostNames[0][host] != "" {
					return "chosen by hand: " + hostNames[0][host]
				}
				return "chosen by hand: host " + host
			case "none":
				return "none"
			}
			return "from the UniFi controller"
		}
		was := before.Upstream.Mode
		if was == "" {
			was = "auto"
		}
		add("Upstream", say(was, before.Upstream.ManualHost), say(req.Upstream.Mode, req.Upstream.HostID))
	}
	if req.Own != nil {
		add("Asset tag", before.Own.AssetTag, strings.TrimSpace(req.Own.AssetTag))
		add("Location", before.Own.Location, strings.TrimSpace(req.Own.Location))
	}
	if req.Links != nil {
		text := func(ls []linkView) string {
			var parts []string
			for _, l := range ls {
				if strings.TrimSpace(l.Label) != "" {
					parts = append(parts, strings.TrimSpace(l.Label)+" "+strings.TrimSpace(l.URL))
				}
			}
			return strings.Join(parts, ", ")
		}
		add("Links", text(before.Links), text(*req.Links))
	}
	if req.Tags != nil {
		var own []string
		for _, t := range before.Tags {
			if t.From == "" {
				own = append(own, t.Name)
			}
		}
		add("Tags", strings.Join(own, ", "), strings.Join(*req.Tags, ", "))
	}
	if req.CategoryOrder != nil {
		was, now := "the default order", "the default order"
		if len(before.CategoryOrder) > 0 {
			was = strings.Join(before.CategoryOrder, ", ")
		}
		if len(*req.CategoryOrder) > 0 {
			now = strings.Join(*req.CategoryOrder, ", ")
		}
		add("Sensor order", was, now)
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDefault(v, _ string) string {
	if v == "" {
		return "default"
	}
	return v
}

// thresholdText reads a per-host threshold: its own value, or the default it falls back to.
func thresholdText(v, def, unit string) string {
	u := ""
	if unit != "" {
		u = " " + unit
	}
	if v == "" {
		if def == "" {
			return "default"
		}
		return "default (" + def + u + ")"
	}
	return v + u
}

// ifaceSummary lists a host's interfaces compactly: "SNMP 10.0.0.3:161, Agent 10.0.0.3:10050".
func ifaceSummary(ifs []ifaceView) string {
	kinds := map[int]string{1: "Agent", 2: "SNMP", 3: "IPMI", 4: "JMX"}
	var parts []string
	for _, i := range ifs {
		addr := strings.TrimSpace(i.IP)
		if i.UseIP == 0 {
			addr = strings.TrimSpace(i.DNS)
		}
		port := strings.TrimSpace(i.Port)
		if port == "" {
			port = defaultPort(i.Type)
		}
		p := fmt.Sprintf("%s %s:%s", kinds[i.Type], addr, port)
		if i.Type == 2 && i.SNMP != nil && !i.Inherit {
			p += fmt.Sprintf(" v%d", i.SNMP.Version)
		}
		if i.Type == 2 && i.Inherit {
			p += " (probe defaults)"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// channelDiff logs what a channel edit changed. Its credentials (the config) read only "changed".
func channelDiff(r *http.Request, was, now store.NotifyChannel) {
	sites := func(ss []string) string {
		if len(ss) == 0 {
			return "all sites"
		}
		return strings.Join(ss, ", ")
	}
	mins := func(m int) string {
		if m == 0 {
			return "at once"
		}
		return humanDur(int64(m) * 60)
	}
	changeDiff(r, "Name", was.Name, now.Name)
	changeDiff(r, "Sites", sites(was.Sites), sites(now.Sites))
	tags := func(ts []string) string {
		if len(ts) == 0 {
			return "any"
		}
		return strings.Join(ts, ", ")
	}
	changeDiff(r, "Tags", tags(was.Tags), tags(now.Tags))
	changeDiff(r, "Alerts", severityFloorLabel(was.MinSeverity), severityFloorLabel(now.MinSeverity))
	changeDiff(r, "Notify after", mins(was.DelayMin), mins(now.DelayMin))
	rep := func(m int) string {
		if m == 0 {
			return "off"
		}
		return "every " + humanDur(int64(m)*60)
	}
	changeDiff(r, "Remind", rep(was.RepeatMin), rep(now.RepeatMin))
	changeDiff(r, "Problem alerts", onOff(was.Alerts), onOff(now.Alerts))
	changeDiff(r, "System notices", onOff(was.Notices), onOff(now.Notices))
	changeDiff(r, "Who to call", onOff(was.WhoToCall), onOff(now.WhoToCall))
	for k, v := range now.Config {
		if was.Config[k] != v {
			changeDiff(r, "Connection", "", "changed")
			break
		}
	}
}

// severityFloorLabel reads a channel's alert floor the way its editor offers it.
func severityFloorLabel(sev int) string {
	switch {
	case sev <= 0:
		return "none"
	case sev <= 2:
		return "Warnings and errors"
	default:
		return "Errors only"
	}
}
