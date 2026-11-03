// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package diag runs the diagnostics an admin or helpdesk user starts from the UI: a ping, a
// traceroute or a DNS lookup. The core runs them here, in process (its image has no shell and no
// network tools); a probe runs its own twin, argus_diag.py, and posts the same Result shape back.
//
// Ping and traceroute use an unprivileged datagram ICMP socket, the one the network scanner uses
// (works as the container's nonroot user under Docker's default ping_group_range); the traceroute
// reads the routers' "time exceeded" answers from the socket's error queue (IP_RECVERR). Both are
// IPv4, the way Argus's checks resolve names first. A DNS lookup asks a server directly and the
// system resolver too, so an answer the checks never see shows up as the two differing.
package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"time"
)

// Request is one diagnostic as the UI asks for it, and as a probe gets it from its check-in.
type Request struct {
	Tool   string `json:"tool"`             // ping | traceroute | dns
	Target string `json:"target"`           // a host name or an address
	Count  int    `json:"count,omitempty"`  // ping: how many echoes (1 to 20)
	Type   string `json:"type,omitempty"`   // dns: both (A + AAAA) | A | AAAA | CNAME | MX | TXT | PTR
	Server string `json:"server,omitempty"` // dns: the server to ask, an address ("" = the source's own)
}

// Result is what a run found so far; Done is set on the last one. Raw is the run as text, the way
// the tool prints it, for the "Show the raw output" fold.
type Result struct {
	Addr  string `json:"addr,omitempty"` // what the target resolved to (ping, traceroute)
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
	Raw   string `json:"raw"`
	Ping  *Ping  `json:"ping,omitempty"`
	Trace *Trace `json:"trace,omitempty"`
	DNS   *DNS   `json:"dns,omitempty"`
}

// Ping is each echo sent so far, answered or not.
type Ping struct {
	Replies []Reply `json:"replies"`
}

// Reply is one echo: MS is nil when no reply came back in time.
type Reply struct {
	Seq  int      `json:"seq"`
	From string   `json:"from,omitempty"`
	MS   *float64 `json:"ms,omitempty"`
}

// Trace is the path so far; Reached is set when the target itself answered.
type Trace struct {
	Hops    []Hop `json:"hops"`
	Reached bool  `json:"reached"`
}

// Hop is one step of the path: Addr is "" when nothing answered at that distance. Note says why a
// trace stopped there (a router answered that the target can't be reached).
type Hop struct {
	N    int      `json:"n"`
	Addr string   `json:"addr,omitempty"`
	Name string   `json:"name,omitempty"`
	MS   *float64 `json:"ms,omitempty"`
	Note string   `json:"note,omitempty"`
}

// DNS is what the server said (Answers) and what the system resolver the checks use said (System).
type DNS struct {
	Server  string   `json:"server"`
	Answers []Answer `json:"answers"`
	System  []Answer `json:"system"`
	Differ  bool     `json:"differ"`
}

// Answer is one record, or why a type has none (Error).
type Answer struct {
	Type  string  `json:"type"`
	Value string  `json:"value,omitempty"`
	TTL   *uint32 `json:"ttl,omitempty"`
	Error string  `json:"error,omitempty"`
}

// Tools and DNS types a request may name.
var (
	tools    = map[string]bool{"ping": true, "traceroute": true, "dns": true}
	dnsTypes = map[string]bool{"both": true, "A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "PTR": true}
	// A host name or an address: letters, digits, dots, hyphens, colons (IPv6), starting and ending
	// with a letter or digit, so nothing reads as a tool's option.
	targetRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._:-]{0,251}[A-Za-z0-9])?$`)
)

// MaxHops is how far a traceroute goes.
const MaxHops = 30

// Clean checks a request and fills in its defaults; the error says what is wrong in the user's words.
func Clean(r Request) (Request, error) {
	r.Tool = strings.ToLower(strings.TrimSpace(r.Tool))
	r.Target = strings.TrimSuffix(strings.TrimSpace(r.Target), ".")
	r.Server = strings.TrimSpace(r.Server)
	if !tools[r.Tool] {
		return r, errors.New("pick ping, traceroute or DNS lookup")
	}
	if !targetRE.MatchString(r.Target) {
		return r, errors.New("the target must be a host name or an address")
	}
	switch r.Tool {
	case "ping":
		if r.Count <= 0 {
			r.Count = 5
		}
		r.Count = min(r.Count, 20)
		r.Type, r.Server = "", ""
	case "traceroute":
		r.Count, r.Type, r.Server = 0, "", ""
	case "dns":
		r.Count = 0
		if r.Type == "" {
			r.Type = "both"
		}
		if r.Type != "both" {
			r.Type = strings.ToUpper(r.Type)
		}
		if !dnsTypes[r.Type] {
			return r, errors.New("pick a record type")
		}
		if r.Type == "PTR" {
			if _, err := netip.ParseAddr(r.Target); err != nil {
				return r, errors.New("a PTR lookup needs an address as its target")
			}
		}
		if r.Server != "" {
			if _, err := netip.ParseAddr(r.Server); err != nil {
				return r, errors.New("the DNS server must be an address")
			}
		}
	}
	return r, nil
}

// Run runs a cleaned request, calling progress with each partial result (it may be nil) and
// returning the final one. It ends when ctx does.
func Run(ctx context.Context, r Request, progress func(Result)) Result {
	if progress == nil {
		progress = func(Result) {}
	}
	var res Result
	switch r.Tool {
	case "ping":
		res = runPing(ctx, r, progress)
	case "traceroute":
		res = runTrace(ctx, r, progress)
	default:
		res = runDNS(ctx, r)
	}
	res.Done = true
	return res
}

// resolve4 is the target's IPv4 address, the first the system resolver gives.
func resolve4(ctx context.Context, target string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(target); err == nil {
		if !a.Unmap().Is4() {
			return netip.Addr{}, errors.New("ping and traceroute from the core are IPv4 only")
		}
		return a.Unmap(), nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(lctx, "ip4", target)
	if err != nil || len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("could not resolve %s: %s", target, lookupWhy(err))
	}
	return ips[0].Unmap(), nil
}

// lookupWhy is a resolver error in plain words.
func lookupWhy(err error) string {
	var de *net.DNSError
	switch {
	case err == nil:
		return "no IPv4 address"
	case errors.As(err, &de) && de.IsNotFound:
		return "no such name"
	case errors.As(err, &de) && de.IsTimeout:
		return "the DNS server did not answer"
	case errors.As(err, &de):
		return de.Err
	}
	return err.Error()
}

// reverseName is an address's PTR name, or "" (kept short: a missing PTR must not stall a trace).
func reverseName(ctx context.Context, addr string) string {
	lctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(lctx, addr)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// ms is a duration in milliseconds, kept to three decimals.
func ms(d time.Duration) *float64 {
	v := float64(d.Microseconds()) / 1000
	return &v
}

// fmtMS is a round trip the way the tools print it.
func fmtMS(v float64) string { return fmt.Sprintf("%.3f ms", v) }

// pingRaw is a ping as busybox prints it.
func pingRaw(target string, addr netip.Addr, p *Ping, done bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PING %s (%s): 56 data bytes\n", target, addr)
	got, lo, hi, sum := 0, 0.0, 0.0, 0.0
	for _, r := range p.Replies {
		if r.MS == nil {
			fmt.Fprintf(&b, "no reply for seq=%d\n", r.Seq)
			continue
		}
		fmt.Fprintf(&b, "64 bytes from %s: seq=%d time=%s\n", r.From, r.Seq, fmtMS(*r.MS))
		if got == 0 || *r.MS < lo {
			lo = *r.MS
		}
		hi = max(hi, *r.MS)
		sum += *r.MS
		got++
	}
	if done && len(p.Replies) > 0 {
		fmt.Fprintf(&b, "\n--- %s ping statistics ---\n%d packets transmitted, %d packets received, %d%% packet loss\n",
			target, len(p.Replies), got, (len(p.Replies)-got)*100/len(p.Replies))
		if got > 0 {
			fmt.Fprintf(&b, "round-trip min/avg/max = %.3f/%.3f/%.3f ms\n", lo, sum/float64(got), hi)
		}
	}
	return b.String()
}

// traceRaw is a traceroute as busybox prints it.
func traceRaw(target string, addr netip.Addr, t *Trace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "traceroute to %s (%s), %d hops max\n", target, addr, MaxHops)
	for _, h := range t.Hops {
		if h.Addr == "" {
			fmt.Fprintf(&b, "%2d  *\n", h.N)
			continue
		}
		name := h.Addr
		if h.Name != "" {
			name = h.Name
		}
		line := fmt.Sprintf("%2d  %s (%s)", h.N, name, h.Addr)
		if h.MS != nil {
			line += "  " + fmtMS(*h.MS)
		}
		if h.Note != "" {
			line += "  !" + h.Note
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// resolvConfServer is the first name server in /etc/resolv.conf, the one the system resolver asks.
func resolvConfServer() string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			if a, err := netip.ParseAddr(f[1]); err == nil {
				return a.String()
			}
		}
	}
	return ""
}
