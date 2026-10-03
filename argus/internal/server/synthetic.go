// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"strings"
	"sync"
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
// Only a sensor that has collected before "stopped": one that never had a value (a process that isn't
// started, a second WAN a gateway doesn't have, a reading the hardware doesn't report) is a sensor that
// doesn't apply, not an outage, so it stays silent - as does one gone quiet for over a day, which
// Zabbix no longer reports a last value for.
//
// A sensor that is supported but gets no new values isn't one of them: many sensors store only
// changes, so their last value can legitimately be hours old. A device or probe that goes silent is
// caught by its master sensor instead.

const (
	synthPrefix      = "argus-"
	synthUnsupported = synthPrefix + "unsupported-" // + item id
	synthInterface   = synthPrefix + "interface-"   // + interface id
	// A sensor alerts on its third failed check in a row: Argus notices the first within a poll, then
	// waits unsupportedChecks more intervals of the sensor's own.
	unsupportedChecks   = 2
	defaultIntervalSecs = 60  // when a sensor's interval can't be read (a macro, a trapper)
	synthSevUnsupported = "4" // High: the sensor is blind
	synthSevInterface   = "4" // High
)

// isSynthetic reports whether an event id is an Argus-raised problem (not a Zabbix event).
func isSynthetic(eventID string) bool { return strings.HasPrefix(eventID, synthPrefix) }

// synthSet is the Argus-raised problems of one moment, with their targets and readings (Zabbix's
// error message, which says more than any value would).
type synthSet struct {
	problems []zabbix.Problem
	targets  map[string]zabbix.TriggerTarget
	readings map[string]string
	// silent: Argus problem ids for sensors that are not supported but don't alert (never collected, or
	// not failing long enough yet). The notifier keeps their baseline and drops any alert already sent
	// for them without a recovery notice.
	silent map[string]bool
	// complete: both lookups answered, so the set is the whole truth (the incident log closes what's
	// missing from it only then).
	complete bool
}

// syntheticProblems gathers the Argus-raised problems. record = true (the notifier) keeps the
// "unsupported since" table in step; the read-only API paths pass false and only look it up, so a
// sensor they see first isn't counted as unsupported for 10 minutes yet.
func syntheticProblems(ctx context.Context, st *store.Store, zbx *zabbix.Client, record bool) synthSet {
	if !record {
		// The read-only callers (Overview, host list, sensor census, host page) poll from every open
		// browser; share one lookup between them for a few seconds.
		synthCache.mu.Lock()
		defer synthCache.mu.Unlock()
		if time.Since(synthCache.at) < synthCacheTTL {
			return synthCache.set
		}
		synthCache.set, synthCache.at = collectSynthetic(ctx, st, zbx, false), time.Now()
		return synthCache.set
	}
	return collectSynthetic(ctx, st, zbx, true)
}

const synthCacheTTL = 15 * time.Second

var synthCache struct {
	mu  sync.Mutex
	at  time.Time
	set synthSet
}

// leftOverUnsupported reports a dependent sensor whose "not supported" is left over from its master:
// its own steps can't fail (each discards its value, or sets one, on error), so only a failure of its
// master made it unsupported, and Zabbix keeps that state until the sensor stores a value again. A
// URL that doesn't answer never gives its response time or status code one, so after a single failed
// check they'd read "stopped collecting" for as long as the URL stays down. With the master collecting
// again, that is no sensor that stopped: the master's own sensors say what is wrong.
// plumbingKeys are items that feed Argus itself rather than being read as sensors: the UniFi wired
// client list (upstream.go). One that fails just leaves what it feeds unknown, so it never alerts.
var plumbingKeys = map[string]bool{"unifi.clients": true}

func leftOverUnsupported(it zabbix.UnsupportedItem, master zabbix.MasterItem) bool {
	if master.State != "0" || len(it.Preprocessing) == 0 {
		return false
	}
	for _, p := range it.Preprocessing {
		switch {
		case p.Type == "19" || p.Type == "20": // discard unchanged (with heartbeat): never fails
		case p.ErrorHandler == "1" || p.ErrorHandler == "2": // discard, or set a value, on error
		default:
			return false
		}
	}
	return true
}

func collectSynthetic(ctx context.Context, st *store.Store, zbx *zabbix.Client, record bool) synthSet {
	out := synthSet{targets: map[string]zabbix.TriggerTarget{}, readings: map[string]string{}, silent: map[string]bool{}}
	now := time.Now().Unix()

	items, uerr := zbx.UnsupportedItems(ctx)
	if uerr == nil {
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
		// A dependent sensor has no interval of its own: it is collected with its master.
		var masters []string
		for _, it := range items {
			if it.MasterItemID != "" && it.MasterItemID != "0" {
				masters = append(masters, it.MasterItemID)
			}
		}
		masterOf, _ := zbx.MasterItems(ctx, masters)
		for _, it := range items {
			start, ok := since[it.ItemID]
			delay := it.Delay
			m, dep := masterOf[it.MasterItemID]
			if dep {
				delay = m.Delay
			}
			id := synthUnsupported + it.ItemID
			if plumbingKeys[it.Key] {
				out.silent[id] = true // feeds Argus itself (the upstream device), not a reading anyone watches
				continue
			}
			if dep && leftOverUnsupported(it, m) {
				out.silent[id] = true
				continue
			}
			// A collector alerts even when it never collected: its sensors only exist once it runs.
			_, isColl := collectorOf(it.Key)
			if !ok || (atoi64(it.LastClock) == 0 && !isColl) || now-start < int64(unsupportedChecks)*intervalSecs(delay) || len(it.Hosts) == 0 {
				out.silent[id] = true
				continue
			}
			out.problems = append(out.problems, zabbix.Problem{
				EventID: id, ObjectID: id, Name: sensorLabel(it.Key, it.Name) + " stopped collecting",
				Severity: synthSevUnsupported, Clock: itoa64(start), Acknowledged: "0",
			})
			out.targets[id] = zabbix.TriggerTarget{Hosts: it.Hosts, Items: []zabbix.TargetItem{{ItemID: it.ItemID, Key: it.Key}}}
			out.readings[id] = unsupportedReading(it.Error)
			if isColl {
				out.readings[id] = "Not supported: " + collectorWhy(it.Key, it.Error)
			}
		}
	}

	ifaces, ierr := zbx.UnavailableInterfaces(ctx)
	if ierr == nil {
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
	out.complete = uerr == nil && ierr == nil
	return out
}

// intervalSecs reads a Zabbix update interval ("30s", "1m", "2h", "90", or a flexible "1m;50s/1-5,9:00-18:00"
// whose first part is the regular interval) as seconds; a macro, "0" or anything unreadable counts as
// one minute.
func intervalSecs(delay string) int64 {
	d := strings.TrimSpace(delay)
	if i := strings.IndexByte(d, ';'); i >= 0 {
		d = d[:i]
	}
	if d == "" {
		return defaultIntervalSecs
	}
	mult := int64(1)
	switch d[len(d)-1] {
	case 's':
		d = d[:len(d)-1]
	case 'm':
		mult, d = 60, d[:len(d)-1]
	case 'h':
		mult, d = 3600, d[:len(d)-1]
	case 'd':
		mult, d = 86400, d[:len(d)-1]
	case 'w':
		mult, d = 7*86400, d[:len(d)-1]
	}
	n := atoi64(d)
	if n <= 0 {
		return defaultIntervalSecs
	}
	return n * mult
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

// unsupportedReading is a "stopped collecting" alert's reading: Zabbix's reason, trimmed (itemError).
func unsupportedReading(msg string) string {
	msg = itemError(msg)
	if msg == "" {
		return "Not supported"
	}
	return "Not supported: " + msg
}

// sensorLabel names a sensor the way the tree does ("Disk temperature · nvme0"), falling back to its
// Zabbix name.
func sensorLabel(key, name string) string {
	if c, ok := collectorOf(key); ok {
		return c.label
	}
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
