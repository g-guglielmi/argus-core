// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package diag

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestCleanRefusesWhatCouldReadAsAnOption(t *testing.T) {
	bad := []Request{
		{Tool: "ping", Target: "-f"},
		{Tool: "ping", Target: "host;reboot"},
		{Tool: "ping", Target: "a b"},
		{Tool: "ping", Target: "$(id)"},
		{Tool: "ping", Target: ""},
		{Tool: "ping", Target: strings.Repeat("a", 254)},
		{Tool: "telnet", Target: "web1"},
		{Tool: "dns", Target: "web1", Type: "ANY"},
		{Tool: "dns", Target: "web1", Type: "PTR"},
		{Tool: "dns", Target: "web1", Server: "dns.example.lan"},
	}
	for _, r := range bad {
		if _, err := Clean(r); err == nil {
			t.Errorf("Clean(%+v) took it", r)
		}
	}
	good := map[Request]Request{
		{Tool: "Ping", Target: " nas1.example.lan. "}:                         {Tool: "ping", Target: "nas1.example.lan", Count: 5},
		{Tool: "ping", Target: "10.0.0.20", Count: 99}:                        {Tool: "ping", Target: "10.0.0.20", Count: 20},
		{Tool: "traceroute", Target: "fe80::1", Count: 3}:                     {Tool: "traceroute", Target: "fe80::1"},
		{Tool: "dns", Target: "nas1.example.lan"}:                             {Tool: "dns", Target: "nas1.example.lan", Type: "both"},
		{Tool: "dns", Target: "10.0.0.20", Type: "ptr"}:                       {Tool: "dns", Target: "10.0.0.20", Type: "PTR"},
		{Tool: "dns", Target: "example.lan", Type: "mx", Server: "10.0.0.53"}: {Tool: "dns", Target: "example.lan", Type: "MX", Server: "10.0.0.53"},
	}
	for in, want := range good {
		got, err := Clean(in)
		if err != nil || got != want {
			t.Errorf("Clean(%+v) = %+v, %v; want %+v", in, got, err, want)
		}
	}
}

func TestReverseZone(t *testing.T) {
	if got := reverseZone(netip.MustParseAddr("10.0.0.20")); got != "20.0.0.10.in-addr.arpa" {
		t.Errorf("v4: %s", got)
	}
	if got := reverseZone(netip.MustParseAddr("2001:db8::1")); !strings.HasPrefix(got, "1.0.0.0.") || !strings.HasSuffix(got, ".8.b.d.0.1.0.0.2.ip6.arpa") {
		t.Errorf("v6: %s", got)
	}
}

// The case the lookup is for: the server has the A record and no AAAA, and the system resolver
// (a musl one) reads the AAAA NXDOMAIN as "no such name" for both.
func TestDifferSeesAnAnswerTheChecksMiss(t *testing.T) {
	server := []Answer{{Type: "A", Value: "10.0.0.20"}, {Type: "AAAA", Error: "NXDOMAIN: the name does not exist"}}
	if !differ(server, []Answer{{Type: "A", Error: "no such name"}}) {
		t.Error("a missing A on the system side is a difference")
	}
	if differ(server, []Answer{{Type: "A", Value: "10.0.0.20"}, {Type: "AAAA", Error: "no such name"}}) {
		t.Error("the same A and no AAAA on both sides is no difference")
	}
	if !differ(server, []Answer{{Type: "A", Value: "10.0.0.21"}}) {
		t.Error("another address is a difference")
	}
}

// fakeDNS answers A with 10.0.0.20 (and a CNAME before it) and AAAA with NXDOMAIN.
func fakeDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no UDP socket:", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
				continue
			}
			m := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true}, Questions: q.Questions}
			qn := q.Questions[0].Name
			target := dnsmessage.MustNewName("nas1.example.lan.")
			switch q.Questions[0].Type {
			case dnsmessage.TypeA:
				m.Answers = []dnsmessage.Resource{
					{Header: dnsmessage.ResourceHeader{Name: qn, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.CNAMEResource{CNAME: target}},
					{Header: dnsmessage.ResourceHeader{Name: target, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 300}, Body: &dnsmessage.AResource{A: [4]byte{10, 0, 0, 20}}},
				}
			default:
				m.RCode = dnsmessage.RCodeNameError
			}
			b, _ := m.Pack()
			_, _ = pc.WriteTo(b, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestQueryServerReadsAnswersAndRcodes(t *testing.T) {
	host, port, _ := net.SplitHostPort(fakeDNS(t))
	old := dnsPort
	dnsPort = port
	t.Cleanup(func() { dnsPort = old })
	ans, rcode, err := queryServer(context.Background(), host, "nas.example.lan", dnsmessage.TypeA)
	if err != nil || rcode != dnsmessage.RCodeSuccess {
		t.Fatal(rcode, err)
	}
	var got []string
	for _, a := range ans {
		got = append(got, a.Type+" "+a.Value)
	}
	if strings.Join(got, ", ") != "CNAME nas1.example.lan, A 10.0.0.20" || *ans[1].TTL != 300 {
		t.Errorf("answers: %v", got)
	}
	if _, rcode, err := queryServer(context.Background(), host, "nas.example.lan", dnsmessage.TypeAAAA); err != nil || rcodeWhy(rcode) != "NXDOMAIN: the name does not exist" {
		t.Errorf("AAAA: %v %v", rcode, err)
	}
}

// An answer that carries another question's id is not ours.
func TestExchangeRefusesAnotherID(t *testing.T) {
	q, _ := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 8},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("nas.example.lan."), Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}}}).Pack()
	if _, err := exchange(context.Background(), "udp", fakeDNS(t), q, 9); err == nil {
		t.Error("took an answer to another question")
	}
}

func TestPingRawReadsLikeBusybox(t *testing.T) {
	one, two := 0.412, 0.9
	p := &Ping{Replies: []Reply{{Seq: 0, From: "10.0.0.20", MS: &one}, {Seq: 1}, {Seq: 2, From: "10.0.0.20", MS: &two}}}
	raw := pingRaw("nas1.example.lan", netip.MustParseAddr("10.0.0.20"), p, true)
	for _, want := range []string{
		"PING nas1.example.lan (10.0.0.20): 56 data bytes",
		"64 bytes from 10.0.0.20: seq=0 time=0.412 ms",
		"no reply for seq=1",
		"3 packets transmitted, 2 packets received, 33% packet loss",
		"round-trip min/avg/max = 0.412/0.656/0.900 ms",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw lacks %q:\n%s", want, raw)
		}
	}
}

func TestTraceRawMarksSilentHops(t *testing.T) {
	v := 4.0
	tr := &Trace{Hops: []Hop{{N: 1, Addr: "10.0.0.1", Name: "gateway.example.lan", MS: &v}, {N: 2}, {N: 3, Addr: "192.0.2.1", MS: &v, Note: "host unreachable"}}}
	raw := traceRaw("nas1.example.lan", netip.MustParseAddr("10.0.0.20"), tr)
	for _, want := range []string{" 1  gateway.example.lan (10.0.0.1)  4.000 ms", " 2  *", " 3  192.0.2.1 (192.0.2.1)  4.000 ms  !host unreachable"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw lacks %q:\n%s", want, raw)
		}
	}
}

// Ping and traceroute to the loopback: one hop, reached. Skipped where the kernel refuses
// unprivileged ICMP sockets (and on dev builds off Linux).
func TestLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := Run(ctx, Request{Tool: "ping", Target: "127.0.0.1", Count: 2}, nil)
	if p.Error != "" {
		t.Skip("no unprivileged ICMP here:", p.Error)
	}
	if !p.Done || len(p.Ping.Replies) != 2 || p.Ping.Replies[0].MS == nil {
		t.Errorf("ping: %+v", p.Ping)
	}
	tr := Run(ctx, Request{Tool: "traceroute", Target: "127.0.0.1"}, nil)
	if tr.Error != "" || !tr.Trace.Reached || len(tr.Trace.Hops) != 1 || tr.Trace.Hops[0].Addr != "127.0.0.1" {
		t.Errorf("traceroute: %+v %s", tr.Trace, tr.Error)
	}
}
