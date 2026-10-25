// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"argus/internal/store"
	"argus/internal/zabbix"
)

// A UniFi controller that restarts (its Network application updating) or drops off the network takes
// every device read through it with it: the API answers with the devices marked disconnected, or
// doesn't answer at all. Each device would then alert on its own, one "offline on the controller" or
// "stopped collecting" per access point, switch and gateway, and the same again when they return. That
// is one event, the controller's, so Argus reads it as one: when enough of a controller's devices show
// that signature at once, it raises one problem on the controller's own host (the console, else the
// gateway, else the first device) and holds the devices' alerts behind it, the way a down probe holds
// its site. A device still offline once the controller is back alerts on its own after the settle
// window. The devices of a controller are the hosts that share its {$UNIFI.URL}.

const (
	unifiURLMacro   = "{$UNIFI.URL}"
	synthController = synthPrefix + "controller-" // + a hash of the controller URL
	controllerKey   = "argus.controller"          // the pseudo sensor the fold is on, for the hold
	// controllerQuorum is how many devices must show the controller's signature at once, and they
	// must be at least half of the controller's devices: two access points re-adopting on their own
	// are two problems, not the controller's.
	controllerQuorum = 2
)

// unifiGroup is one controller and the hosts monitored through it.
type unifiGroup struct {
	url     string   // normalised: scheme://host[:port], lower case
	host    string   // the controller's own host, when it is monitored (the console, else the gateway); "" when not
	devices []string // every host with that URL, the controller's own included, sorted by host id
}

// controllerID is the Argus problem id of a controller's outage (stable per URL across ticks).
func controllerID(u string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(u))
	return synthController + fmt.Sprintf("%08x", h.Sum32())
}

// controllerLabel names a controller by its host part: "unifi.example.lan:11443".
func controllerLabel(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// normaliseControllerURL makes the same controller written two ways one group.
func normaliseControllerURL(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if p, err := url.Parse(s); err == nil && p.Host != "" {
		return p.Scheme + "://" + p.Host
	}
	return strings.TrimRight(s, "/")
}

// groupUniFi is the grouping itself: hosts by their controller URL, the controller's own host chosen
// as the console with that URL, else a gateway (the UniFi OS console is a gateway as a rule).
func groupUniFi(macros map[string]string, classes map[string]string) []unifiGroup {
	byURL := map[string]*unifiGroup{}
	console, gateway := map[string]string{}, map[string]string{}
	for hostID, raw := range macros {
		u := normaliseControllerURL(raw)
		if u == "" {
			continue
		}
		g := byURL[u]
		if g == nil {
			g = &unifiGroup{url: u}
			byURL[u] = g
		}
		g.devices = append(g.devices, hostID)
		switch classes[hostID] {
		case "unifi-console":
			if console[u] == "" || hostID < console[u] {
				console[u] = hostID
			}
		case "unifi-gateway":
			if gateway[u] == "" || hostID < gateway[u] {
				gateway[u] = hostID
			}
		}
	}
	out := make([]unifiGroup, 0, len(byURL))
	for u, g := range byURL {
		sort.Strings(g.devices)
		g.host = console[u]
		if g.host == "" {
			g.host = gateway[u]
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].url < out[j].url })
	return out
}

// unifiCache serves the grouping to the notifier and the read-only pages alike for a few seconds.
var unifiCache struct {
	mu     sync.Mutex
	at     time.Time
	groups []unifiGroup
}

// loadUniFiGroups reads every host's {$UNIFI.URL} and groups them. Best effort: nothing readable means
// no groups, so no fold.
func loadUniFiGroups(ctx context.Context, st *store.Store, zbx *zabbix.Client) []unifiGroup {
	unifiCache.mu.Lock()
	defer unifiCache.mu.Unlock()
	if time.Since(unifiCache.at) < synthCacheTTL {
		return unifiCache.groups
	}
	var groups []unifiGroup
	if macros, err := zbx.UserMacroHosts(ctx, unifiURLMacro); err == nil && len(macros) > 0 {
		classes, _ := st.DeviceClasses(ctx)
		groups = groupUniFi(macros, classes)
	}
	unifiCache.groups, unifiCache.at = groups, time.Now()
	return groups
}

// deviceSignal is what one UniFi host says about its controller.
type deviceSignal struct {
	connected int  // its "controller state" reading: 1 connected, 0 not, -1 unknown (never read)
	pingUp    bool // its ping (true when it has none): a device that doesn't ping is its own problem
	rawBroken bool // the sensor that calls the controller has stopped (not supported for long enough)
}

// affected reports the controller's signature on a device: reported offline while still answering,
// or the controller call itself failing.
func (d deviceSignal) affected() bool {
	return (d.connected == 0 && d.pingUp) || d.rawBroken
}

// controllerOutage is one controller read as restarting or unreachable, with the devices showing it.
type controllerOutage struct {
	group    unifiGroup
	affected []string // host ids, sorted
}

// controllerOutages finds the controllers whose devices show the signature in numbers.
func controllerOutages(groups []unifiGroup, signals map[string]deviceSignal) []controllerOutage {
	var out []controllerOutage
	for _, g := range groups {
		var hit []string
		for _, h := range g.devices {
			if signals[h].affected() {
				hit = append(hit, h)
			}
		}
		if len(hit) >= controllerQuorum && 2*len(hit) >= len(g.devices) {
			out = append(out, controllerOutage{group: g, affected: hit})
		}
	}
	return out
}

// controllerClock keeps when each controller's outage was first seen and when it last ended, so the
// problem keeps its start across ticks and the devices wait out the settle window after it.
var controllerClock struct {
	mu        sync.Mutex
	since     map[string]int64 // url -> first seen
	recovered map[string]int64 // url -> when the last outage ended
}

// controllerSince notes an outage seen now and returns when it began.
func controllerSince(u string, now int64) int64 {
	controllerClock.mu.Lock()
	defer controllerClock.mu.Unlock()
	if controllerClock.since == nil {
		controllerClock.since = map[string]int64{}
	}
	if s, ok := controllerClock.since[u]; ok {
		return s
	}
	controllerClock.since[u] = now
	return now
}

// controllerEnded notes which controllers are in outage now; the ones that no longer are get a
// recovery time.
func controllerEnded(open map[string]bool, now int64) {
	controllerClock.mu.Lock()
	defer controllerClock.mu.Unlock()
	for u := range controllerClock.since {
		if !open[u] {
			delete(controllerClock.since, u)
			if controllerClock.recovered == nil {
				controllerClock.recovered = map[string]int64{}
			}
			controllerClock.recovered[u] = now
		}
	}
}

// controllerOpen reports whether any controller outage was open at the last look (so a tick that
// finds none still closes it).
func controllerOpen() bool {
	controllerClock.mu.Lock()
	defer controllerClock.mu.Unlock()
	return len(controllerClock.since) > 0
}

// controllerRecoveredAt is when a controller's last outage ended (0 = none known since the core started).
func controllerRecoveredAt(u string) int64 {
	controllerClock.mu.Lock()
	defer controllerClock.mu.Unlock()
	return controllerClock.recovered[u]
}

// unifiSignals reads every grouped host's signals: the controller-state and ping readings, and the
// controller-calling sensors that stopped (the unsupported list with how long each has failed).
func unifiSignals(ctx context.Context, zbx *zabbix.Client, groups []unifiGroup, unsupported []zabbix.UnsupportedItem, since map[string]int64, now int64) map[string]deviceSignal {
	member := map[string]bool{}
	for _, g := range groups {
		for _, h := range g.devices {
			member[h] = true
		}
	}
	signals := map[string]deviceSignal{}
	for h := range member {
		signals[h] = deviceSignal{connected: -1, pingUp: true}
	}
	if items, err := zbx.ItemsByKeys(ctx, []string{"unifi.device.state", defaultMasterKey}); err == nil {
		for _, it := range items {
			d, ok := signals[it.HostID]
			if !ok || it.Status != "0" {
				continue
			}
			switch it.Key {
			case "unifi.device.state":
				if it.State == "0" && atoi64(it.LastClock) > 0 {
					if strings.TrimSpace(it.LastValue) == "0" {
						d.connected = 0
					} else {
						d.connected = 1
					}
				}
			case defaultMasterKey:
				if it.State == "0" && atoi64(it.LastClock) > 0 && strings.TrimSpace(it.LastValue) == "0" {
					d.pingUp = false
				}
			}
			signals[it.HostID] = d
		}
	}
	for _, it := range unsupported {
		if !strings.HasPrefix(it.Key, "unifi.") || !strings.HasSuffix(it.Key, ".raw") {
			continue
		}
		start, ok := since[it.ItemID]
		if !ok || now-start < int64(unsupportedChecks)*intervalSecs(it.Delay) {
			continue
		}
		for _, h := range it.Hosts {
			if d, member := signals[h.HostID]; member {
				d.rawBroken = true
				signals[h.HostID] = d
			}
		}
	}
	return signals
}

// controllerProblems turns the outages into Argus problems on the controllers' hosts: one each, held
// open from its first sighting, with the affected devices as its reading. hostName names hosts for
// the reading; hostOf gives a problem's host record.
func controllerProblems(outages []controllerOutage, hostName map[string]string, hostOf func(id string) (zabbix.TargetHost, bool), now int64) ([]zabbix.Problem, map[string]zabbix.TriggerTarget, map[string]string) {
	var problems []zabbix.Problem
	targets, readings := map[string]zabbix.TriggerTarget{}, map[string]string{}
	open := map[string]bool{}
	for _, o := range outages {
		owner := o.group.host
		if owner == "" {
			owner = o.group.devices[0]
		}
		h, ok := hostOf(owner)
		if !ok {
			continue
		}
		open[o.group.url] = true
		id := controllerID(o.group.url)
		names := make([]string, 0, len(o.affected))
		for _, a := range o.affected {
			if n := hostName[a]; n != "" {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		problems = append(problems, zabbix.Problem{
			EventID: id, ObjectID: id,
			Name:     fmt.Sprintf("UniFi controller %s: %d of %d devices offline on it", controllerLabel(o.group.url), len(o.affected), len(o.group.devices)),
			Severity: synthSevInterface, Clock: itoa64(controllerSince(o.group.url, now)), Acknowledged: "0",
		})
		targets[id] = zabbix.TriggerTarget{Hosts: []zabbix.TargetHost{h}}
		readings[id] = "The controller is restarting or unreachable; offline on it: " + strings.Join(names, ", ")
	}
	controllerEnded(open, now)
	return problems, targets, readings
}

// controllerUpName is the RESOLVED sentence for a controller outage.
func controllerUpName(name string) string {
	if !strings.HasPrefix(name, "UniFi controller ") {
		return ""
	}
	if i := strings.LastIndex(name, ": "); i > 0 { // the label may carry a port ("host:11443")
		return name[:i] + ": its devices are back on it"
	}
	return name + ": its devices are back on it"
}
