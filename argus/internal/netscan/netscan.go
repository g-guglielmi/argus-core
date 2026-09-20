// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package netscan is the core-side network discovery scanner (§B): the in-process Go twin of the
// probe's argus_netscan.py, used when a scan runs from "Core server" instead of a probe (no
// check-in channel to ride, so the Argus server sweeps the subnet itself). It reports the same raw
// fact shape - reachability, a small TCP service-port set, SNMP v1/v2c system OIDs, an HTTP(S)
// banner, a real DNS query and reverse DNS - and the caller classifies with provision.SuggestClass.
// Differences from the probe scanner, both container-imposed: ICMP uses an unprivileged datagram
// socket (available when the runtime allows net.ipv4.ping_group_range, which Docker does by
// default; silently skipped otherwise, so ICMP-only devices may be missed there), and MAC
// addresses are never reported (a bridged container's ARP table doesn't see the LAN).
package netscan

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxHosts caps a scan's subnet size (a /22) - matched by the API-side CIDR validation and
	// the probe scanner.
	MaxHosts = 1024
	workers  = 64
)

// tcpPorts is the fingerprint service-port set - keep in sync with argus_netscan.py.
var tcpPorts = []int{22, 53, 80, 443, 445, 3493, 8080, 8443, 10050}

// SNMPCred is the community credential a scan fingerprints with (v1/v2c only, like the probe).
type SNMPCred struct {
	Version   int
	Community string
	Port      int
}

// SNMPInfo is the SNMP system group of a host that answered.
type SNMPInfo struct {
	SysDescr    string
	SysObjectID string
	SysName     string
}

// HTTPBanner is the first answering web port's banner. The JSON shape matches the probe scanner's
// http object verbatim (it is stored raw on the discovery result and read by the review UI).
type HTTPBanner struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
	Status int    `json:"status"`
	Server string `json:"server"`
	Title  string `json:"title"`
}

// Host is one live address's raw fingerprint.
type Host struct {
	IP   string
	RDNS string
	TCP  []int
	SNMP *SNMPInfo
	HTTP *HTTPBanner
	DNS  bool
}

// Scan sweeps an IPv4 CIDR and returns the live hosts sorted by address. partial reports that the
// context deadline cut the sweep short (the results so far are still valid).
func Scan(ctx context.Context, cidr string, cred *SNMPCred) (hosts []Host, partial bool, err error) {
	p, perr := netip.ParsePrefix(strings.TrimSpace(cidr))
	if perr != nil || !p.Addr().Is4() {
		return nil, false, fmt.Errorf("invalid IPv4 CIDR %q", cidr)
	}
	targets := hostsInPrefix(p, MaxHosts)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, a := range targets {
		if ctx.Err() != nil {
			partial = true
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a netip.Addr) {
			defer wg.Done()
			defer func() { <-sem }()
			if h := scanHost(ctx, a, cred); h != nil {
				mu.Lock()
				hosts = append(hosts, *h)
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	if ctx.Err() != nil {
		partial = true
	}
	sort.Slice(hosts, func(i, j int) bool {
		ai, _ := netip.ParseAddr(hosts[i].IP)
		aj, _ := netip.ParseAddr(hosts[j].IP)
		return ai.Compare(aj) < 0
	})
	return hosts, partial, nil
}

// hostsInPrefix enumerates the prefix's usable addresses (network + broadcast excluded for /30 and
// wider, like ipaddress.hosts()), capped at max.
func hostsInPrefix(p netip.Prefix, max int) []netip.Addr {
	p = p.Masked()
	var out []netip.Addr
	for a := p.Addr(); p.Contains(a) && len(out) < max+2; a = a.Next() {
		out = append(out, a)
	}
	if p.Bits() <= 30 && len(out) >= 2 {
		out = out[1 : len(out)-1] // drop network + broadcast
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func scanHost(ctx context.Context, a netip.Addr, cred *SNMPCred) *Host {
	if ctx.Err() != nil {
		return nil
	}
	ip := a.String()
	alive, _ := pingProbe(a, time.Second)
	var open []int
	for _, port := range tcpPorts {
		if ctx.Err() != nil {
			break
		}
		if tcpOpen(ip, port) {
			open = append(open, port)
		}
	}
	var info *SNMPInfo
	if cred != nil && cred.Community != "" {
		info = snmpGet(ip, cred)
	}
	if !alive && len(open) == 0 && info == nil {
		return nil
	}
	if open == nil {
		open = []int{}
	}
	h := &Host{IP: ip, TCP: open, SNMP: info, RDNS: rdns(ctx, ip)}
	if hasPort(open, 53) && dnsAnswers(ip) {
		h.DNS = true
	}
	h.HTTP = httpBanner(ip, open)
	return h
}

func tcpOpen(ip string, port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, fmt.Sprint(port)), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func hasPort(ports []int, p int) bool {
	for _, x := range ports {
		if x == p {
			return true
		}
	}
	return false
}

func rdns(ctx context.Context, ip string) string {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(c, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// dnsAnswers sends a real A query; any well-formed reply (even REFUSED) proves a DNS service.
func dnsAnswers(ip string) bool {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(ip, "53"), 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	tid := uint16(rand.Intn(0x10000))
	pkt := []byte{byte(tid >> 8), byte(tid), 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range []string{"example", "com"} {
		pkt = append(pkt, byte(len(label)))
		pkt = append(pkt, label...)
	}
	pkt = append(pkt, 0x00, 0, 1, 0, 1) // root, QTYPE=A, QCLASS=IN
	if _, err := conn.Write(pkt); err != nil {
		return false
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	return err == nil && n >= 12 && uint16(buf[0])<<8|uint16(buf[1]) == tid
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var wsRe = regexp.MustCompile(`\s+`)

// httpBanner grabs the first answering web port's status/Server/<title> (https first - richer
// titles than an http redirect stub). Redirects are NOT followed, matching the probe scanner.
func httpBanner(ip string, ports []int) *HTTPBanner {
	candidates := []struct {
		port   int
		scheme string
	}{{443, "https"}, {80, "http"}, {8443, "https"}, {8080, "http"}}
	for _, c := range candidates {
		if !hasPort(ports, c.port) {
			continue
		}
		tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
		client := &http.Client{
			Timeout:   3 * time.Second,
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s://%s/", c.scheme, net.JoinHostPort(ip, fmt.Sprint(c.port))), nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "argus-netscan")
		resp, err := client.Do(req)
		if err != nil {
			tr.CloseIdleConnections()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		tr.CloseIdleConnections()
		title := ""
		if m := titleRe.FindSubmatch(body); m != nil {
			title = strings.TrimSpace(wsRe.ReplaceAllString(string(m[1]), " "))
			if len(title) > 120 {
				title = title[:120]
			}
		}
		srv := resp.Header.Get("Server")
		if len(srv) > 80 {
			srv = srv[:80]
		}
		return &HTTPBanner{Port: c.port, Scheme: c.scheme, Status: resp.StatusCode, Server: srv, Title: title}
	}
	return nil
}
