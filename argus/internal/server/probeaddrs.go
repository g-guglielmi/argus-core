// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"net/netip"
	"strings"

	"argus/internal/store"
)

// A probe's LAN and public address, the line under its name on the Probes page. Three sources, the
// most exact first:
//   - a probe VM's host reports its LAN address and gateway with its OS status (argus-os-report);
//   - the probe container's minute report carries the address it reaches out from, and now and then
//     its public one (a lookup the probe makes itself, every half hour and when its address changes);
//   - Argus sees where that report comes from.
//
// A container on Docker's bridge network (the usual docker run, a probe VM's too) reaches out from a
// 172.17.x address that means nothing on the site; behind its host's NAT, Argus sees the host's
// address instead, which is the one to show when it is a private one (the report came through the VPN
// or the core's own network).

// probeAddrView is one address on the Probes page: the address, where it came from (host: the probe
// VM's own report; probe: the probe container's report or its lookup; seen: where Argus sees the
// probe's report come from), the gateway when known and when it was last reported.
type probeAddrView struct {
	IP  string `json:"ip"`
	Src string `json:"src"`
	GW  string `json:"gw,omitempty"`
	At  int64  `json:"at,omitempty"`
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

// probeLAN picks the LAN address to show: the VM host's report; else the container's own address
// when it is a site one (host networking, a macvlan, or the very address Argus sees); else the
// private address Argus sees the report come from (the container's host, behind its NAT). nil when
// none is known: a container on a bridge whose report comes over the internet.
func probeLAN(a store.ProbeAddrs) *probeAddrView {
	switch {
	case a.Host != "":
		return &probeAddrView{IP: a.Host, Src: "host", GW: a.HostGW, At: a.HostAt}
	case privateAddr(a.Own) && (a.Own == a.Seen || !inDockerNets(a.Own)):
		return &probeAddrView{IP: a.Own, Src: "probe", At: a.At}
	case privateAddr(a.Seen):
		return &probeAddrView{IP: a.Seen, Src: "seen", At: a.At}
	}
	return nil
}

// probePublic picks the public address to show: the probe's own lookup; else the public address
// Argus sees the report come from. nil when neither is known (a probe that reports over the VPN
// and couldn't look it up).
func probePublic(a store.ProbeAddrs) *probeAddrView {
	switch {
	case a.Public != "":
		return &probeAddrView{IP: a.Public, Src: "probe", At: a.PublicAt}
	case publicAddr(a.Seen):
		return &probeAddrView{IP: a.Seen, Src: "seen", At: a.At}
	}
	return nil
}
