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

	"argus/internal/store"
	"argus/internal/zabbix"
)

// Upstream devices (DESIGN section 7f): the device a host is plugged into. By default it is the UniFi
// controller's answer: a UniFi switch or access point reports the device it hangs off (its uplink
// MAC and port), and a switch or gateway lists the wired clients on its ports, which Argus matches to
// hosts by IP (else by the MAC discovery saw). A host can be set to one chosen by hand, or to none.
// While an upstream device is down, the hosts behind it are held like a down master holds its host's
// sensors: Argus can't reach them through it anyway, and the upstream's own alert says it all.

// upstreamItemKeys are the items the controller's answer is read from.
var upstreamItemKeys = []string{"unifi.mac", "unifi.uplink.mac", "unifi.uplink.port", "unifi.clients"}

// upstreamLink is a host's upstream device: which host, on which of its ports, and who said so.
type upstreamLink struct {
	Host   string
	Port   string
	Source string // controller | manual
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

// loadUpstreams reads the controller's answer and the hosts' settings: the upstream in effect for each
// host, and the controller's answer on its own. Best effort: what can't be read is just unknown.
func loadUpstreams(ctx context.Context, st *store.Store, zbx *zabbix.Client) (eff, auto map[string]upstreamLink) {
	auto = map[string]upstreamLink{}
	if items, err := zbx.ItemsByKeys(ctx, upstreamItemKeys); err == nil {
		ips, _ := zbx.HostIPs(ctx)
		macs, _ := st.DiscoveredMACs(ctx)
		auto = controllerUpstreams(items, ips, macs)
	}
	settings, _ := st.HostUpstreams(ctx)
	return effectiveUpstreams(auto, settings), auto
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
	eff  map[string]upstreamLink
	auto map[string]upstreamLink
}

func (s *Server) upstreams(ctx context.Context) (eff, auto map[string]upstreamLink) {
	s.ups.mu.Lock()
	defer s.ups.mu.Unlock()
	if s.ups.eff != nil && time.Since(s.ups.at) < time.Minute {
		return s.ups.eff, s.ups.auto
	}
	s.ups.eff, s.ups.auto = loadUpstreams(ctx, s.st, s.zbx)
	s.ups.at = time.Now()
	return s.ups.eff, s.ups.auto
}

func (s *Server) forgetUpstreams() {
	s.ups.mu.Lock()
	s.ups.at = time.Time{}
	s.ups.mu.Unlock()
}

// startUpstreamRefresh keeps the controller's answers on record every few minutes, and logs it when one
// changes (the first answer for a host is just stored).
func (s *Server) startUpstreamRefresh(ctx context.Context) {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(90 * time.Second):
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

// recordUpstreams stores the controller's current answers and logs the ones that changed.
func (s *Server) recordUpstreams(ctx context.Context) {
	s.forgetUpstreams()
	_, auto := s.upstreams(ctx)
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
	for h := range hosts {
		cur := auto[h]
		was := settings[h]
		if cur.Host == was.AutoHost && cur.Port == was.AutoPort {
			continue
		}
		if err := s.st.SetAutoUpstream(ctx, h, cur.Host, cur.Port, now); err != nil {
			continue
		}
		if was.AutoAt == 0 {
			continue // the first answer for this host: nothing changed, it's just known now
		}
		old := label(upstreamLink{Host: was.AutoHost, Port: was.AutoPort})
		c := store.Change{Category: "hosts", Action: "Upstream device", Object: idx[h].Name, HostIDs: []string{h},
			Diff: []store.ChangeDiff{{Field: "Upstream (from the UniFi controller)", Old: old, New: label(cur)}}}
		if cur.Host == "" {
			c.Diff[0].New = "none"
		}
		if was.Mode == "manual" || was.Mode == "none" {
			c.Detail = "not in effect: this host's upstream is set by hand"
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
	Auto       *hopView      `json:"auto,omitempty"`   // the controller's answer
	Path       []hopView     `json:"path"`             // the chain in effect, top first, ending with this host
	Behind     []hostRefView `json:"behind"`           // the hosts plugged straight into this one
	BehindAll  int           `json:"behind_all"`       // every host whose chain goes through this one
	Source     string        `json:"source,omitempty"` // controller | manual
	Why        string        `json:"why,omitempty"`    // why the controller gives none, when it doesn't
}

type hostRefView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// hostUpstreamView is a host's upstream as its Device tab and settings show it.
func (s *Server) hostUpstreamView(ctx context.Context, hostID string) upstreamView {
	eff, auto := s.upstreams(ctx)
	settings, _ := s.st.HostUpstreams(ctx)
	idx, _ := s.hostIndex(ctx)
	down := s.downHosts(ctx)
	v := upstreamView{Mode: "auto", Path: []hopView{}, Behind: []hostRefView{}}
	if u, ok := settings[hostID]; ok && u.Mode != "" {
		v.Mode, v.ManualHost = u.Mode, u.ManualHost
	}
	if a, ok := auto[hostID]; ok {
		v.Auto = &hopView{HostID: a.Host, Name: idx[a.Host].Name, Port: a.Port}
	}
	if l, ok := eff[hostID]; ok {
		v.Source = l.Source
	}
	if v.Auto == nil && v.Mode == "auto" {
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
