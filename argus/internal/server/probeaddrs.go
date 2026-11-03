// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/netip"
	"strings"
	"time"

	"argus/internal/store"
)

// A probe's LAN and public address, the line under its name on the Probes page. The sources, the
// most exact first:
//   - the kind of Docker network the proxy container is on, from the argus-updater sidecar's
//     check-in: on a macvlan, an ipvlan or the host's network the container's own address is the LAN
//     one (a container with a dedicated address on the site);
//   - the host's LAN address and gateway: a probe VM's OS report (argus-os-report), or the sidecar's
//     check-in (on a bridge it reads them with the host's network when it starts and after each
//     start of the proxy);
//   - the probe container's minute report: the address it reaches out from, and the last public one
//     its own lookup found (every hour and when its address changes), with its age;
//   - where Argus sees that report come from.
//
// A container on Docker's bridge network (the usual docker run, a probe VM's too) reaches out from a
// 172.17.x address that means nothing on the site. The address Argus sees is no LAN address either:
// any NAT on the way (the host's, a VPN router's, a Tailscale exit node's) puts its own there. So it
// is never shown as the LAN address, only said on hover.

// probeAddrView is one address on the Probes page: the address, where it came from (host: the host's
// report, the probe VM's or the sidecar's; probe: the probe container's report or its lookup; seen:
// where Argus sees the probe's report come from), the gateway when known and when it was reported.
type probeAddrView struct {
	IP  string `json:"ip"`
	Src string `json:"src"`
	GW  string `json:"gw,omitempty"`
	At  int64  `json:"at,omitempty"`
	Net string `json:"net,omitempty"` // the container's network kind, for its own address (the sidecar's word)
}

// cleanNet is a Docker network driver name as the sidecar reports it, or "" when it isn't one.
func cleanNet(s string) string {
	switch s = strings.TrimSpace(s); s {
	case "bridge", "macvlan", "ipvlan", "host", "overlay":
		return s
	}
	return ""
}

// cleanAddr is s as a canonical unicast IP address, or "" when it isn't one (empty, garbage, a
// loopback, unspecified, multicast or link-local address: none says where the probe is).
func cleanAddr(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() {
		return ""
	}
	return a.String()
}

// cgnat is the shared address space carriers and overlay VPNs (Tailscale) use: not on the internet.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// dockerNets is where Docker hands out bridge networks by default (172.17.0.0/16 first, then the
// rest of 172.16.0.0/12): an address there, reported from inside a container, is most likely the
// container's own on a bridge, not one on the site.
var dockerNets = netip.MustParsePrefix("172.16.0.0/12")

// privateAddr reports whether a (a cleanAddr result) is not reachable from the internet.
func privateAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && (a.IsPrivate() || cgnat.Contains(a))
}

// publicAddr reports whether a (a cleanAddr result) is an internet address.
func publicAddr(s string) bool {
	return s != "" && !privateAddr(s)
}

func inDockerNets(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && dockerNets.Contains(a)
}

// probeLAN picks the LAN address to show. The container's own address comes first when it is one on
// the site: the sidecar says the container is on a macvlan, an ipvlan or the host's network, or, with
// no word from a sidecar, the address is private and outside Docker's bridge pools (or the very one
// Argus sees). Else the host's, when private. nil when none is known, with why: "public_host" (the
// host's own address is a public one: a VPS on the internet has no LAN) or "bridge" (a container on
// a bridge, with no sidecar to read the host's).
func probeLAN(a store.ProbeAddrs) (*probeAddrView, string) {
	own := privateAddr(a.Own)
	switch a.Net {
	case "macvlan", "ipvlan", "host":
	case "bridge", "overlay":
		own = false // a bridge of the user's may well hand out 192.168.x: never a site address
	default:
		own = own && (a.Own == a.Seen || !inDockerNets(a.Own))
	}
	switch {
	case own:
		return &probeAddrView{IP: a.Own, Src: "probe", At: a.At, Net: a.Net}, ""
	case privateAddr(a.Host):
		return &probeAddrView{IP: a.Host, Src: "host", GW: a.HostGW, At: a.HostAt}, ""
	case publicAddr(a.Host):
		return nil, "public_host"
	case a.Own != "" || a.Seen != "":
		return nil, "bridge"
	}
	return nil, ""
}

// probePublic picks the public address to show: the probe's own lookup; else a host whose own address
// is public (a VPS); else the public address Argus sees the report come from. nil when none is known
// (a probe that reports over the VPN and couldn't look it up).
func probePublic(a store.ProbeAddrs) *probeAddrView {
	switch {
	case a.Public != "":
		return &probeAddrView{IP: a.Public, Src: "probe", At: a.PublicAt}
	case publicAddr(a.Host):
		return &probeAddrView{IP: a.Host, Src: "host", At: a.HostAt}
	case publicAddr(a.Seen):
		return &probeAddrView{IP: a.Seen, Src: "seen", At: a.At}
	}
	return nil
}

// publicFoundAt is when the probe's lookup found the public address it reports: now less the age it
// gives (held to 0..30 days), or now when it gives none (r19 sent it right after the lookup).
func publicFoundAt(age *int64, now time.Time) int64 {
	if age == nil || *age < 0 || *age > 30*86400 {
		return now.Unix()
	}
	return now.Unix() - *age
}
