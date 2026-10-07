// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"argus/internal/provision"
	"argus/internal/store"
	"argus/internal/zabbix"
)

// Upstream devices (DESIGN section 7f): the device a host is plugged into. By default it is automatic:
// the UniFi controller's answer (a UniFi switch or access point reports the device it hangs off, its
// uplink MAC and port, and a switch or gateway lists the wired clients on its ports, which Argus
// matches to hosts by IP, else by the MAC discovery saw), and for a host that is a VM, the hypervisor
// it runs on (an XCP-NG pool lists its VMs' network cards; that answer wins, the controller only
// seeing the VM's MAC on the hypervisor's switch port). A host can be set to one chosen by hand, or to
// none.
// While an upstream device is down, the hosts behind it are held like a down master holds its host's
// sensors: Argus can't reach them through it anyway, and the upstream's own alert says it all.

// upstreamItemKeys are the items the controller's answer is read from.
var upstreamItemKeys = []string{"unifi.mac", "unifi.uplink.mac", "unifi.uplink.port", "unifi.uplink.local", "unifi.clients", "xcp.vm.nics"}

// upstreamLink is a host's upstream device: which host, on which of its ports, and who said so.
type upstreamLink struct {
	Host   string
	Port   string
	Source string // controller | xcpng (the hypervisor it runs on) | manual
	// Doubt is why the controller's answer doesn't add up against the port it names (doubtUpstreams):
	// Argus ignores such an answer and keeps the one it has.
	Doubt string
}

type wiredClient struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Port any    `json:"port"`
}

func portText(v any) string {
	switch p := v.(type) {
	case float64:
		if p > 0 {
			return fmt.Sprintf("%d", int(p))
		}
	case string:
		return strings.TrimSpace(p)
	}
	return ""
}

// controllerUpstreams works out each host's upstream from what the UniFi controller reports.
func controllerUpstreams(items []zabbix.Item, ips map[string]string, discMACs map[string]string) map[string]upstreamLink {
	macHost := map[string]string{}
	uplinkMAC := map[string]string{}
	uplinkPort := map[string]string{}
	clients := map[string][]wiredClient{}
	for _, it := range items {
		switch it.Key {
		case "unifi.mac":
			if m := normMAC(it.LastValue); len(m) == 12 {
				macHost[m] = it.HostID
			}
		case "unifi.uplink.mac":
			if m := normMAC(it.LastValue); len(m) == 12 {
				uplinkMAC[it.HostID] = m
			}
		case "unifi.uplink.port":
			uplinkPort[it.HostID] = strings.TrimSpace(it.LastValue)
		case "unifi.clients":
			var cs []wiredClient
			if json.Unmarshal([]byte(it.LastValue), &cs) == nil {
				clients[it.HostID] = cs
			}
		}
	}
	for id, m := range discMACs {
		if n := normMAC(m); len(n) == 12 {
			if _, ok := macHost[n]; !ok {
				macHost[n] = id
			}
		}
	}
	// Hosts that share an address are one machine (a NAS and the services on it): a client at that
	// address is all of them.
	ipHosts := map[string][]string{}
	for id, ip := range ips {
		ipHosts[ip] = append(ipHosts[ip], id)
	}
	out := map[string]upstreamLink{}
	for h, m := range uplinkMAC {
		if up := macHost[m]; up != "" && up != h {
			out[h] = upstreamLink{Host: up, Port: uplinkPort[h], Source: "controller"}
		}
	}
	// Wired clients, in a stable order so a client two devices list (it shouldn't) settles the same way.
	sws := make([]string, 0, len(clients))
	for sw := range clients {
		sws = append(sws, sw)
	}
	sort.Strings(sws)
	for _, sw := range sws {
		for _, c := range clients[sw] {
			var hs []string
			if c.IP != "" {
				hs = ipHosts[c.IP]
			}
			if len(hs) == 0 {
				if h := macHost[normMAC(c.MAC)]; h != "" {
					hs = []string{h}
				}
			}
			for _, h := range hs {
				if h == sw {
					continue
				}
				if _, has := out[h]; has {
					continue // a UniFi device's own uplink says it better
				}
				out[h] = upstreamLink{Host: sw, Port: portText(c.Port), Source: "controller"}
			}
		}
	}
	return out
}

// vmNicList is an XCP-NG pool's VM list (xcp.vm.nics): its members, and each VM's network cards,
// guest addresses and the member it runs on.
type vmNicList struct {
	Members []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"members"`
	VMs []struct {
		Name string   `json:"name"`
		Host string   `json:"host"`
		MACs []string `json:"macs"`
		IPs  []string `json:"ips"`
	} `json:"vms"`
}

// hypervisorUpstreams places the hosts that are VMs under the hypervisor they run on, from each
// XCP-NG pool's VM list (on the pool's host). A VM is matched to hosts by its guest addresses, and by
// its MACs: through the UniFi wired-client lists (MAC to address) or the MACs discovery saw. Its
// hypervisor is the host at the pool member's address, or the pool's own host when the pool has just
// that member; a VM on a member Argus doesn't monitor is left to the other answers.
func hypervisorUpstreams(items []zabbix.Item, ips map[string]string, discMACs map[string]string) map[string]upstreamLink {
	ipHosts := map[string][]string{}
	for id, ip := range ips {
		ipHosts[ip] = append(ipHosts[ip], id)
	}
	for _, hs := range ipHosts {
		sort.Strings(hs)
	}
	macIP := map[string]string{}
	var pools []zabbix.Item
	for _, it := range items {
		switch it.Key {
		case "unifi.clients":
			var cs []wiredClient
			if json.Unmarshal([]byte(it.LastValue), &cs) == nil {
				for _, c := range cs {
					if m := normMAC(c.MAC); len(m) == 12 && c.IP != "" {
						macIP[m] = c.IP
					}
				}
			}
		case "xcp.vm.nics":
			pools = append(pools, it)
		}
	}
	macHost := map[string]string{}
	for id, m := range discMACs {
		if n := normMAC(m); len(n) == 12 {
			macHost[n] = id
		}
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].HostID < pools[j].HostID })
	out := map[string]upstreamLink{}
	for _, it := range pools {
		var list vmNicList
		if json.Unmarshal([]byte(it.LastValue), &list) != nil {
			continue
		}
		member := map[string]string{}
		for _, m := range list.Members {
			hs := ipHosts[strings.TrimSpace(m.Address)]
			switch {
			case len(hs) > 0:
				member[m.Name] = hs[0]
				for _, h := range hs {
					if h == it.HostID {
						member[m.Name] = h
					}
				}
			case len(list.Members) == 1:
				member[m.Name] = it.HostID
			}
		}
		for _, vm := range list.VMs {
			hv := member[vm.Host]
			if hv == "" {
				continue
			}
			var hs []string
			for _, ip := range vm.IPs {
				hs = append(hs, ipHosts[strings.TrimSpace(ip)]...)
			}
			for _, mac := range vm.MACs {
				n := normMAC(mac)
				if ip := macIP[n]; ip != "" {
					hs = append(hs, ipHosts[ip]...)
				}
				if h := macHost[n]; h != "" {
					hs = append(hs, h)
				}
			}
			for _, h := range hs {
				if h == hv || h == it.HostID {
					continue
				}
				if _, has := out[h]; !has {
					out[h] = upstreamLink{Host: hv, Source: "xcpng"}
				}
			}
		}
	}
	return out
}

// doubtUpstreams checks the controller's answers against the ports they name. The controller guesses a
// device's uplink from what the switches have learned, and a guess can be plainly wrong: two USW Flex
// Minis on one switch were each placed on a port of the other with no link, or on the other's own
// uplink port. A device can't hang off a port with no link, nor off the port its upstream uses to
// reach its own upstream (that would put it above, not below). ownUplink is each switch's own uplink
// port (unifi.uplink.local), portUp the link state ("1" / "0") of the ports the answers name; what
// isn't known isn't held against an answer.
func doubtUpstreams(links map[string]upstreamLink, ownUplink map[string]string, portUp map[string]map[string]string) {
	for h, l := range links {
		if l.Port == "" {
			continue
		}
		switch {
		case ownUplink[l.Host] == l.Port:
			l.Doubt = "that is the port it uses for its own uplink"
		case portUp[l.Host][l.Port] == "0":
			l.Doubt = "that port has no link"
		default:
			continue
		}
		links[h] = l
	}
}

// upstreamPortStates reads the link state of the ports the answers name: host -> port -> "1" / "0",
// from the switches' (and gateways') port items.
func upstreamPortStates(ctx context.Context, zbx *zabbix.Client, answers ...map[string]upstreamLink) map[string]map[string]string {
	hostSet, keySet := map[string]bool{}, map[string]bool{}
	for _, links := range answers {
		for _, l := range links {
			if l.Port != "" {
				hostSet[l.Host] = true
				keySet["unifi.port.state["+l.Port+"]"] = true
			}
		}
	}
	hosts := make([]string, 0, len(hostSet))
	for h := range hostSet {
		hosts = append(hosts, h)
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	out := map[string]map[string]string{}
	items, err := zbx.HostItemsByKeys(ctx, hosts, keys)
	if err != nil {
		return out
	}
	for _, it := range items {
		v := strings.TrimSpace(it.LastValue)
		if it.State != "0" || it.LastClock == "" || it.LastClock == "0" || (v != "0" && v != "1") {
			continue // not read yet, or not supported: unknown
		}
		_, p := splitKey(it.Key)
		if out[it.HostID] == nil {
			out[it.HostID] = map[string]string{}
		}
		out[it.HostID][param(p, 0)] = v
	}
	return out
}

// effectiveUpstreams applies each host's setting to the controller's answer.
func effectiveUpstreams(auto map[string]upstreamLink, settings map[string]store.HostUpstream) map[string]upstreamLink {
	out := make(map[string]upstreamLink, len(auto))
	for h, l := range auto {
		out[h] = l
	}
	for h, u := range settings {
		switch u.Mode {
		case "none":
			delete(out, h)
		case "manual":
			if u.ManualHost != "" && u.ManualHost != h {
				out[h] = upstreamLink{Host: u.ManualHost, Source: "manual"}
			} else {
				delete(out, h)
			}
		}
	}
	return out
}

// upstreamSettle is how long the controller must keep giving a host a new upstream before Argus takes
// it. Its answer can flip every few minutes (two USW Flex Minis on one switch report no neighbours,
// so the controller guesses each hangs off the other as their switch's MAC table ages out), and
// taking every flip moved the hosts held behind them and filled the Changes log.
const upstreamSettle = 15 * 60

// upstreamFlapAfter flips of one host's answer within upstreamFlapWindow are logged, once a day, as
// "keeps changing" (each flip on its own isn't a change: Argus kept its answer).
const (
	upstreamFlapAfter  = 3
	upstreamFlapWindow = 60 * 60
	upstreamFlapRelog  = 24 * 60 * 60
)

// recordedUpstreams is the automatic answer recordUpstreams last took for each host.
func recordedUpstreams(settings map[string]store.HostUpstream) map[string]upstreamLink {
	out := map[string]upstreamLink{}
	for h, u := range settings {
		if u.AutoAt != 0 && u.AutoHost != "" {
			src := u.AutoSource
			if src == "" {
				src = "controller"
			}
			out[h] = upstreamLink{Host: u.AutoHost, Port: u.AutoPort, Source: src}
		}
	}
	return out
}

// settledUpstreams is the controller's answer as Argus holds it: the one recordUpstreams last took (it
// held for upstreamSettle), and the live one for a host with none taken yet. A recorded answer that
// fails the port checks now (wrong: one taken before the checks, or since proven impossible) is no
// answer: a live one that passes stands in until recordUpstreams takes it, else the host has none.
func settledUpstreams(live map[string]upstreamLink, settings map[string]store.HostUpstream, wrong map[string]string) map[string]upstreamLink {
	out := map[string]upstreamLink{}
	for h, l := range live {
		if settings[h].AutoAt == 0 && l.Doubt == "" {
			out[h] = l
		}
	}
	for h, l := range recordedUpstreams(settings) {
		if _, bad := wrong[h]; bad {
			if lv, ok := live[h]; ok && lv.Doubt == "" {
				out[h] = lv
			}
			continue
		}
		out[h] = l
	}
	return out
}

// upstreamState is one read of the upstream devices.
type upstreamState struct {
	eff     map[string]upstreamLink // in effect: each host's setting applied to settled
	settled map[string]upstreamLink // the controller's answer as Argus holds it
	live    map[string]upstreamLink // the controller's answer right now (Doubt set where it doesn't add up)
	wrong   map[string]string       // hosts whose recorded answer fails the port checks now, and why
}

// loadUpstreams reads the controller's answer and the hosts' settings. Both the live answers and the
// recorded ones are checked against the ports they name (doubtUpstreams). Best effort: what can't be
// read is just unknown.
func loadUpstreams(ctx context.Context, st *store.Store, zbx *zabbix.Client) upstreamState {
	us := upstreamState{live: map[string]upstreamLink{}, wrong: map[string]string{}}
	settings, _ := st.HostUpstreams(ctx)
	recorded := recordedUpstreams(settings)
	if items, err := zbx.ItemsByKeys(ctx, upstreamItemKeys); err == nil {
		ips, _ := zbx.HostIPs(ctx)
		macs, _ := st.DiscoveredMACs(ctx)
		us.live = controllerUpstreams(items, ips, macs)
		// A VM hangs off its hypervisor: that answer wins over the controller's for the same host.
		for h, l := range hypervisorUpstreams(items, ips, macs) {
			us.live[h] = l
		}
		own := map[string]string{}
		for _, it := range items {
			if it.Key == "unifi.uplink.local" {
				own[it.HostID] = strings.TrimSpace(it.LastValue)
			}
		}
		ports := upstreamPortStates(ctx, zbx, us.live, recorded)
		doubtUpstreams(us.live, own, ports)
		doubtUpstreams(recorded, own, ports)
		for h, l := range recorded {
			if l.Doubt != "" {
				us.wrong[h] = l.Doubt
			}
		}
	}
	us.settled = settledUpstreams(us.live, settings, us.wrong)
	us.eff = effectiveUpstreams(us.settled, settings)
	if classes, err := st.DeviceClasses(ctx); err == nil {
		dropProbeHosts(classes, us.live, us.settled, us.eff)
	}
	return us
}

// dropProbeHosts takes Argus's Probe hosts out of the answers: a Probe host never hangs off anything,
// whoever says so. It is its site's master, and a device it monitors holding its "not reporting"
// alert, while that device's own alerts wait on the probe, would silence both (a probe VM on a
// hypervisor whose ping went down first).
func dropProbeHosts(classes map[string]string, answers ...map[string]upstreamLink) {
	for _, m := range answers {
		for h := range m {
			if c, ok := provision.ClassByID(classes[h]); ok && c.Internal {
				delete(m, h)
			}
		}
	}
}

// upstreamSourceName is who gave an automatic answer, as Changes and the alerts say it.
func upstreamSourceName(src string) string {
	switch src {
	case "xcpng":
		return "XCP-NG"
	case "manual":
		return "set by hand"
	}
	return "the UniFi controller"
}

// upstreamPending is a new answer the controller has been giving a host, and since when.
type upstreamPending struct {
	link  upstreamLink
	since int64
}

// upstreamStep is what one read of the controller does to a host's recorded answer: take the live one
// (the first answer, one replacing a recorded answer that fails the port checks now, or a new one that
// has held for upstreamSettle), or wait on it. flipped is a new answer that went away before it held
// (back to the recorded one, or on to a third).
func upstreamStep(was store.HostUpstream, wasWrong bool, cur upstreamLink, pending *upstreamPending, now int64) (take bool, next *upstreamPending, flipped bool) {
	same := func(a upstreamLink, host, port string) bool { return a.Host == host && a.Port == port }
	switch {
	case same(cur, was.AutoHost, was.AutoPort):
		return false, nil, pending != nil
	case was.AutoAt == 0, wasWrong:
		return true, nil, false
	case pending == nil || !same(cur, pending.link.Host, pending.link.Port):
		return false, &upstreamPending{link: cur, since: now}, pending != nil
	case now-pending.since >= upstreamSettle:
		return true, nil, false
	}
	return false, pending, false
}

// upstreamTrack is recordUpstreams' memory between reads: the answer each host is moving to, the
// times a new one went away before it held, and when "keeps changing" was last logged.
type upstreamTrack struct {
	mu      sync.Mutex
	pending map[string]*upstreamPending
	flips   map[string][]int64
	flapLog map[string]int64
}

// noteFlip records a flip and reports how many the host had in the last upstreamFlapWindow, and
// whether that is worth a "keeps changing" entry now.
func (t *upstreamTrack) noteFlip(h string, now int64) (n int, log bool) {
	if t.flips == nil {
		t.flips, t.flapLog = map[string][]int64{}, map[string]int64{}
	}
	var kept []int64
	for _, at := range t.flips[h] {
		if now-at < upstreamFlapWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	t.flips[h] = kept
	if last, logged := t.flapLog[h]; len(kept) >= upstreamFlapAfter && (!logged || now-last >= upstreamFlapRelog) {
		t.flapLog[h] = now
		return len(kept), true
	}
	return len(kept), false
}

// upstreamChain is a host's upstream devices, nearest first. A chain that loops back (two devices
// that report each other) is no chain: nil, so it never holds anything.
func upstreamChain(m map[string]upstreamLink, host string) []string {
	var out []string
	seen := map[string]bool{host: true}
	for cur := host; len(out) < 16; {
		l, ok := m[cur]
		if !ok {
			return out
		}
		if seen[l.Host] {
			return nil
		}
		seen[l.Host] = true
		out = append(out, l.Host)
		cur = l.Host
	}
	return out
}

// behindHosts is every host whose chain goes through up (nearest first: the ones plugged straight in,
// then theirs).
func behindHosts(m map[string]upstreamLink, up string) []string {
	var out []string
	for h := range m {
		for _, u := range upstreamChain(m, h) {
			if u == up {
				out = append(out, h)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// upstreamCache keeps the upstream map for the pages (the notifier reads its own each tick).
type upstreamCache struct {
	mu   sync.Mutex
	at   time.Time
	st   upstreamState
	read bool
}

// upstreams is the upstream in effect for each host, and the controller's answer as Argus holds it.
func (s *Server) upstreams(ctx context.Context) (eff, auto map[string]upstreamLink) {
	us := s.upstreamState(ctx)
	return us.eff, us.settled
}

func (s *Server) upstreamState(ctx context.Context) upstreamState {
	s.ups.mu.Lock()
	defer s.ups.mu.Unlock()
	if s.ups.read && time.Since(s.ups.at) < time.Minute {
		return s.ups.st
	}
	s.ups.st = loadUpstreams(ctx, s.st, s.zbx)
	s.ups.at, s.ups.read = time.Now(), true
	return s.ups.st
}

func (s *Server) forgetUpstreams() {
	s.ups.mu.Lock()
	s.ups.at = time.Time{}
	s.ups.mu.Unlock()
}

// startUpstreamRefresh reads the controller's answers 90 s after start and then every 5 minutes, and
// records them (recordUpstreams). The first read waits on its own timer: a fresh 90 s wait on every
// turn of the loop used to win over the ticker, so it read every 90 s.
func (s *Server) startUpstreamRefresh(ctx context.Context) {
	go func() {
		first := time.NewTimer(90 * time.Second)
		defer first.Stop()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-t.C:
			}
			if s.zbx.Authenticated() {
				c, cancel := context.WithTimeout(ctx, time.Minute)
				s.recordUpstreams(c)
				cancel()
			}
		}
	}()
}

// recordUpstreams takes the controller's current answers: a host's first answer at once, a new one once
// it has held for upstreamSettle (logged as a change), and a host whose answer keeps flipping is logged
// once a day as such, its recorded answer kept meanwhile.
func (s *Server) recordUpstreams(ctx context.Context) {
	s.forgetUpstreams()
	us := s.upstreamState(ctx)
	auto := us.live
	settings, err := s.st.HostUpstreams(ctx)
	if err != nil {
		return
	}
	idx, _ := s.hostIndex(ctx)
	label := func(l upstreamLink) string {
		if l.Host == "" {
			return ""
		}
		n := idx[l.Host].Name
		if n == "" {
			n = "host " + l.Host
		}
		if l.Port != "" {
			n += " port " + l.Port
		}
		return n
	}
	now := time.Now().Unix()
	hosts := map[string]bool{}
	for h := range auto {
		hosts[h] = true
	}
	for h, u := range settings {
		if u.AutoHost != "" {
			hosts[h] = true
		}
	}
	tr := &s.upsTrack
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.pending == nil {
		tr.pending = map[string]*upstreamPending{}
	}
	for h := range tr.pending {
		if !hosts[h] {
			delete(tr.pending, h)
		}
	}
	for h := range hosts {
		cur := auto[h]
		if cur.Doubt != "" {
			continue // an answer that doesn't add up is no news: keep what Argus has, and any wait
		}
		was := settings[h]
		wrongWhy, wasWrong := us.wrong[h]
		take, next, flipped := upstreamStep(was, wasWrong, cur, tr.pending[h], now)
		if next != nil {
			tr.pending[h] = next
		} else {
			delete(tr.pending, h)
		}
		if flipped {
			if n, log := tr.noteFlip(h, now); log {
				held := label(upstreamLink{Host: was.AutoHost, Port: was.AutoPort})
				if held == "" {
					held = "none"
				}
				c := store.Change{Category: "hosts", Action: "Upstream device keeps changing", Object: idx[h].Name, HostIDs: []string{h},
					Detail: fmt.Sprintf("Its automatic upstream device (%s) changed %d times in the last hour. Argus keeps %s until a new one holds for 15 minutes; setting the upstream device by hand in the host's settings pins it.", upstreamSourceName(cur.Source), n, held)}
				s.logArgusChange(ctx, c)
			}
		}
		if !take {
			continue
		}
		src := cur.Source
		if cur.Host == "" {
			src = ""
		}
		if err := s.st.SetAutoUpstream(ctx, h, cur.Host, cur.Port, src, now); err != nil {
			continue
		}
		if was.AutoAt == 0 {
			continue // the first answer for this host: nothing changed, it's just known now
		}
		old := label(upstreamLink{Host: was.AutoHost, Port: was.AutoPort})
		from := cur.Source
		if cur.Host == "" {
			from = was.AutoSource
		}
		c := store.Change{Category: "hosts", Action: "Upstream device", Object: idx[h].Name, HostIDs: []string{h},
			Diff: []store.ChangeDiff{{Field: "Upstream (from " + upstreamSourceName(from) + ")", Old: old, New: label(cur)}}}
		if cur.Host == "" {
			c.Diff[0].New = "none"
		}
		switch {
		case was.Mode == "manual" || was.Mode == "none":
			c.Detail = "not in effect: this host's upstream is set by hand"
		case wasWrong:
			c.Detail = "the answer it replaces didn't add up: " + wrongWhy
		}
		s.logArgusChange(ctx, c)
	}
}

// --- views ---

type hopView struct {
	HostID string `json:"host_id"`
	Name   string `json:"name"`
	Port   string `json:"port,omitempty"` // the port of this hop the next one is plugged into
	Down   bool   `json:"down,omitempty"`
}

type upstreamView struct {
	Mode       string        `json:"mode"` // auto | manual | none
	ManualHost string        `json:"manual_host,omitempty"`
	Auto       *hopView      `json:"auto,omitempty"`        // the automatic answer
	AutoSource string        `json:"auto_source,omitempty"` // who gave it: controller | xcpng
	Path       []hopView     `json:"path"`                  // the chain in effect, top first, ending with this host
	Behind     []hostRefView `json:"behind"`                // the hosts plugged straight into this one
	BehindAll  int           `json:"behind_all"`            // every host whose chain goes through this one
	Source     string        `json:"source,omitempty"`      // controller | manual
	Why        string        `json:"why,omitempty"`         // why the controller gives none, when it doesn't
	Ignored    string        `json:"ignored,omitempty"`     // the controller's answer right now, when Argus ignores it (and why)
}

type hostRefView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// hostUpstreamView is a host's upstream as its Device tab and settings show it.
func (s *Server) hostUpstreamView(ctx context.Context, hostID string) upstreamView {
	us := s.upstreamState(ctx)
	eff, auto := us.eff, us.settled
	settings, _ := s.st.HostUpstreams(ctx)
	idx, _ := s.hostIndex(ctx)
	down := s.downHosts(ctx)
	v := upstreamView{Mode: "auto", Path: []hopView{}, Behind: []hostRefView{}}
	if u, ok := settings[hostID]; ok && u.Mode != "" {
		v.Mode, v.ManualHost = u.Mode, u.ManualHost
	}
	if a, ok := auto[hostID]; ok {
		v.Auto = &hopView{HostID: a.Host, Name: idx[a.Host].Name, Port: a.Port}
		v.AutoSource = a.Source
	}
	if l, ok := eff[hostID]; ok {
		v.Source = l.Source
	}
	var ignored []string
	if why, bad := us.wrong[hostID]; bad {
		if u := settings[hostID]; u.AutoHost != "" {
			ignored = append(ignored, fmt.Sprintf("Argus dropped the answer it had, %s port %s: %s.", idx[u.AutoHost].Name, u.AutoPort, why))
		}
	}
	if l := us.live[hostID]; l.Doubt != "" {
		ignored = append(ignored, fmt.Sprintf("The UniFi controller now places it on %s port %s, but %s: Argus ignores that answer.", idx[l.Host].Name, l.Port, l.Doubt))
	}
	v.Ignored = strings.Join(ignored, " ")
	if v.Auto == nil && v.Mode == "auto" && v.Ignored != "" {
		v.Why = "The UniFi controller's answers for it don't add up right now."
	} else if v.Auto == nil && v.Mode == "auto" {
		if items, err := s.zbx.ItemsByKeys(ctx, []string{"unifi.clients"}); err == nil {
			ips, _ := s.zbx.HostIPs(ctx)
			names := map[string]string{}
			for id, h := range idx {
				names[id] = h.Name
			}
			var unifiHosts []string
			if classes, err := s.st.DeviceClasses(ctx); err == nil {
				for id, c := range classes {
					if _, ok := idx[id]; ok && (c == "unifi-switch" || c == "unifi-gateway") {
						unifiHosts = append(unifiHosts, id)
					}
				}
			}
			v.Why = upstreamWhy(items, names, ips[hostID], unifiHosts)
		}
	}
	chain := upstreamChain(eff, hostID)
	// top first: each hop shows the port the next one down is plugged into
	below := hostID
	hops := make([]hopView, 0, len(chain)+1)
	for _, up := range chain {
		hops = append(hops, hopView{HostID: up, Name: idx[up].Name, Port: eff[below].Port, Down: down[up]})
		below = up
	}
	for i, j := 0, len(hops)-1; i < j; i, j = i+1, j-1 {
		hops[i], hops[j] = hops[j], hops[i]
	}
	if len(hops) > 0 {
		v.Path = append(hops, hopView{HostID: hostID, Name: idx[hostID].Name, Down: down[hostID]})
	}
	for h, l := range eff {
		if l.Host == hostID {
			v.Behind = append(v.Behind, hostRefView{ID: h, Name: idx[h].Name})
		}
	}
	sort.Slice(v.Behind, func(i, j int) bool { return strings.ToLower(v.Behind[i].Name) < strings.ToLower(v.Behind[j].Name) })
	v.BehindAll = len(behindHosts(eff, hostID))
	return v
}

// upstreamWhy says why the controller gives a host no upstream device, from the UniFi switches' and
// gateways' client lists, naming them: a UniFi switch or gateway with no list (its template not
// updated), a read that failed (with the controller's reason), lists not read yet, or all read and this
// address not in any. unifiHosts is every UniFi switch and gateway.
func upstreamWhy(lists []zabbix.Item, names map[string]string, ip string, unifiHosts []string) string {
	var failed, pending, read []string
	why := ""
	have := map[string]bool{}
	for _, it := range lists {
		if it.Key != "unifi.clients" {
			continue
		}
		have[it.HostID] = true
		switch {
		case it.State == "1":
			failed = append(failed, names[it.HostID])
			if why == "" {
				why = strings.TrimSpace(it.Error)
			}
		case atoi64(it.LastClock) == 0:
			pending = append(pending, names[it.HostID])
		default:
			read = append(read, names[it.HostID])
		}
	}
	var missing []string
	for _, h := range unifiHosts {
		if !have[h] {
			missing = append(missing, names[h])
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s no wired-client list: %s template hasn't been updated (Updates says why, if Zabbix refused it).",
			behindText(missing), plural2(len(missing), "has", "have"), plural2(len(missing), "its", "their")))
	}
	if len(failed) > 0 {
		msg := "Reading the wired clients failed on " + behindText(failed)
		if why != "" {
			msg += ": " + why
		}
		parts = append(parts, msg+".")
	}
	if len(pending) > 0 {
		parts = append(parts, behindText(pending)+" "+plural2(len(pending), "hasn't", "haven't")+" read "+plural2(len(pending), "its", "their")+" wired clients yet (every 10 minutes).")
	}
	switch {
	case len(have) == 0 && len(missing) == 0:
		parts = append(parts, "No UniFi switch or gateway lists its wired clients.")
	case ip == "":
		parts = append(parts, "This host has no IP address to look for among the wired clients.")
	case len(read) > 0:
		parts = append(parts, fmt.Sprintf("The wired clients of %s don't include %s: a device on another brand of switch, or on Wi-Fi, isn't listed.", behindText(read), ip))
	}
	return strings.Join(parts, " ") + " Or pick one in its settings."
}

// plural2 picks a word by count: "has" for one, "have" for more.
func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// downHosts is the hosts whose ping (their main way of being reached) is in error now.
func (s *Server) downHosts(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, err := s.sensorCensus(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if r.key == defaultMasterKey && r.State == "error" {
			out[r.HostID] = true
		}
	}
	return out
}

// behindText names what an upstream holds for its alert: the first few hosts and how many more.
func behindText(names []string) string {
	sort.Strings(names)
	if len(names) <= 4 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
}
