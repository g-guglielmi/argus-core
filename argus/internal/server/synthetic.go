// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"strings"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Argus-raised problems. Zabbix raises no problem when a sensor stops collecting: a "not supported"
// sensor (a collector script that fails, a timeout) just leaves its triggers unknown, and an agent or
// SNMP endpoint that stops answering only marks the host interface unavailable. So nothing alerted.
// Argus turns both into problems of its own, shaped like Zabbix's (a Problem plus a TriggerTarget), so
// the Overview, the host page, acknowledging and the whole notifier (alert delay, master holds,
// escalation, reminders, recovery) treat them like any other.
//
// A sensor that is supported but gets no new values isn't one of them: many sensors store only
// changes, so their last value can legitimately be hours old. A device or probe that goes silent is
// caught by its master sensor instead.

const (
	synthPrefix         = "argus-"
	synthUnsupported    = synthPrefix + "unsupported-" // + item id
	synthInterface      = synthPrefix + "interface-"   // + interface id
	unsupportedAfterSec = 10 * 60                      // a sensor must stay "not supported" this long to alert
	synthSevUnsupported = "2"                          // Warning
	synthSevInterface   = "4"                          // High
)

// isSynthetic reports whether an event id is an Argus-raised problem (not a Zabbix event).
func isSynthetic(eventID string) bool { return strings.HasPrefix(eventID, synthPrefix) }

// synthSet is the Argus-raised problems of one moment, with their targets and readings (Zabbix's
// error message, which says more than any value would).
type synthSet struct {
	problems []zabbix.Problem
	targets  map[string]zabbix.TriggerTarget
	readings map[string]string
}

// syntheticProblems gathers the Argus-raised problems. record = true (the notifier) keeps the
// "unsupported since" table in step; the read-only API paths pass false and only look it up, so a
// sensor they see first isn't counted as unsupported for 10 minutes yet.
func syntheticProblems(ctx context.Context, st *store.Store, zbx *zabbix.Client, record bool) synthSet {
	out := synthSet{targets: map[string]zabbix.TriggerTarget{}, readings: map[string]string{}}
	now := time.Now().Unix()

	if items, err := zbx.UnsupportedItems(ctx); err == nil {
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ItemID)
		}
		var since map[string]int64
		if record {
			since, _ = st.SyncUnsupported(ctx, ids)
		} else {
			since, _ = st.UnsupportedSince(ctx)
		}
		for _, it := range items {
			start, ok := since[it.ItemID]
			if !ok || now-start < unsupportedAfterSec || len(it.Hosts) == 0 {
				continue
			}
			id := synthUnsupported + it.ItemID
			out.problems = append(out.problems, zabbix.Problem{
				EventID: id, ObjectID: id, Name: sensorLabel(it.Key, it.Name) + " stopped collecting",
				Severity: synthSevUnsupported, Clock: itoa64(start), Acknowledged: "0",
			})
			out.targets[id] = zabbix.TriggerTarget{Hosts: it.Hosts, Items: []zabbix.TargetItem{{ItemID: it.ItemID, Key: it.Key}}}
			out.readings[id] = unsupportedReading(it.Error)
		}
	}

	if ifaces, err := zbx.UnavailableInterfaces(ctx); err == nil {
		for _, f := range ifaces {
			if len(f.Hosts) == 0 {
				continue
			}
			start := atoi64(f.ErrorsFrom)
			if start == 0 {
				start = now
			}
			id := synthInterface + f.InterfaceID
			out.problems = append(out.problems, zabbix.Problem{
				EventID: id, ObjectID: id, Name: interfaceDownName(f.Type),
				Severity: synthSevInterface, Clock: itoa64(start), Acknowledged: "0",
			})
			out.targets[id] = zabbix.TriggerTarget{Hosts: f.Hosts}
			out.readings[id] = strings.TrimSpace(f.Error)
		}
	}
	return out
}

// merge adds the Argus-raised problems to Zabbix's (after the Zabbix trigger lookup, which would
// reject their ids).
func (s synthSet) merge(problems []zabbix.Problem, targets map[string]zabbix.TriggerTarget) ([]zabbix.Problem, map[string]zabbix.TriggerTarget) {
	if targets == nil {
		targets = map[string]zabbix.TriggerTarget{}
	}
	for id, t := range s.targets {
		targets[id] = t
	}
	return append(problems, s.problems...), targets
}

// interfaceDownName names an unavailable interface's problem by what stopped answering.
func interfaceDownName(ifaceType string) string {
	switch ifaceType {
	case "1":
		return "Zabbix agent not reachable"
	case "2":
		return "SNMP not responding"
	case "3":
		return "IPMI not reachable"
	case "4":
		return "JMX not reachable"
	}
	return "Monitoring interface not reachable"
}

// unsupportedReading is a "stopped collecting" alert's reading: Zabbix's reason, trimmed to one line.
func unsupportedReading(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return "Not supported"
	}
	return "Not supported: " + msg
}

// sensorLabel names a sensor the way the tree does ("Disk temperature · nvme0"), falling back to its
// Zabbix name.
func sensorLabel(key, name string) string {
	_, l, inst, ch, ok := classifyItem(key, name)
	if !ok || l == "" {
		return name
	}
	// Add the channel (or instance) only when the label doesn't already name it:
	// "Reachable (ICMP)", not "Reachable (ICMP) · Reachable".
	if ch != "" && !strings.Contains(l, ch) {
		return l + " · " + ch
	}
	if ch == "" && inst != "" && !strings.Contains(l, inst) {
		return l + " · " + inst
	}
	return l
}
