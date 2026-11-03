// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package diag

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsQTypes are the record types a lookup type asks for.
var dnsQTypes = map[string][]dnsmessage.Type{
	"both":  {dnsmessage.TypeA, dnsmessage.TypeAAAA},
	"A":     {dnsmessage.TypeA},
	"AAAA":  {dnsmessage.TypeAAAA},
	"CNAME": {dnsmessage.TypeCNAME},
	"MX":    {dnsmessage.TypeMX},
	"TXT":   {dnsmessage.TypeTXT},
	"PTR":   {dnsmessage.TypePTR},
}

// runDNS asks the server for each type, then the system resolver, and says whether they differ.
func runDNS(ctx context.Context, r Request) Result {
	server := r.Server
	if server == "" {
		server = resolvConfServer()
	}
	d := &DNS{Server: server, Answers: []Answer{}, System: []Answer{}}
	res := Result{DNS: d}
	name := r.Target
	if r.Type == "PTR" {
		name = reverseZone(netip.MustParseAddr(r.Target))
	}
	var raw strings.Builder
	if server == "" {
		res.Error = "no DNS server to ask: none was given and the core has none configured"
	} else {
		for _, qt := range dnsQTypes[r.Type] {
			ans, rcode, err := queryServer(ctx, server, name, qt)
			label := typeName(qt)
			switch {
			case err != nil:
				d.Answers = append(d.Answers, Answer{Type: label, Error: err.Error()})
				fmt.Fprintf(&raw, ";; asked %s for %s %s: %s\n", server, name, label, err)
			case rcode != dnsmessage.RCodeSuccess:
				d.Answers = append(d.Answers, Answer{Type: label, Error: rcodeWhy(rcode)})
				fmt.Fprintf(&raw, ";; asked %s for %s %s: %s\n", server, name, label, rcodeName(rcode))
			default:
				fmt.Fprintf(&raw, ";; asked %s for %s %s: NOERROR\n", server, name, label)
				if len(ans) == 0 {
					d.Answers = append(d.Answers, Answer{Type: label, Error: "no " + label + " record"})
				}
				for _, a := range ans {
					d.Answers = append(d.Answers, a)
					fmt.Fprintf(&raw, "%s.\t%d\tIN\t%s\t%s\n", strings.TrimSuffix(name, "."), *a.TTL, a.Type, a.Value)
				}
			}
		}
	}
	d.System = systemLookup(ctx, r)
	for _, a := range d.System {
		if a.Error != "" {
			fmt.Fprintf(&raw, ";; system resolver, %s: %s\n", a.Type, a.Error)
		} else {
			fmt.Fprintf(&raw, ";; system resolver, %s: %s\n", a.Type, a.Value)
		}
	}
	d.Differ = server != "" && len(d.System) > 0 && differ(d.Answers, d.System)
	res.Raw = raw.String()
	return res
}

// dnsPort is the port a server is asked on (a test points it at its own server).
var dnsPort = "53"

// queryServer sends one question over UDP (TCP when the answer was truncated), twice before giving up.
func queryServer(ctx context.Context, server, name string, qt dnsmessage.Type) ([]Answer, dnsmessage.RCode, error) {
	qn, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, 0, errors.New("that name is too long for DNS")
	}
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q, err := (&dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: qn, Type: qt, Class: dnsmessage.ClassINET}},
	}).Pack()
	if err != nil {
		return nil, 0, err
	}
	addr := net.JoinHostPort(server, dnsPort)
	var msg *dnsmessage.Message
	for try := 0; try < 2 && msg == nil; try++ {
		msg, err = exchange(ctx, "udp", addr, q, id)
	}
	if err == nil && msg.Truncated {
		msg, err = exchange(ctx, "tcp", addr, q, id)
	}
	if err != nil {
		return nil, 0, err
	}
	out := []Answer{}
	for _, rr := range msg.Answers {
		if a, ok := answerOf(rr); ok {
			out = append(out, a)
		}
	}
	return out, msg.RCode, nil
}

// exchange is one question and its answer.
func exchange(ctx context.Context, network, addr string, q []byte, id uint16) (*dnsmessage.Message, error) {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(dctx, network, addr)
	if err != nil {
		return nil, errors.New("could not reach the DNS server")
	}
	defer c.Close()
	dl, _ := dctx.Deadline()
	_ = c.SetDeadline(dl)
	buf := make([]byte, 65535)
	var n int
	if network == "tcp" {
		if _, err = c.Write(append([]byte{byte(len(q) >> 8), byte(len(q))}, q...)); err == nil {
			var l [2]byte
			if _, err = readFull(c, l[:]); err == nil {
				n, err = readFull(c, buf[:int(l[0])<<8|int(l[1])])
			}
		}
	} else if _, err = c.Write(q); err == nil {
		n, err = c.Read(buf)
	}
	if err != nil {
		return nil, errors.New("the DNS server did not answer")
	}
	var m dnsmessage.Message
	if err := m.Unpack(buf[:n]); err != nil || m.ID != id || !m.Response {
		return nil, errors.New("the DNS server sent an answer that could not be read")
	}
	return &m, nil
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		k, err := c.Read(b[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// answerOf is a resource record as a row, for the types a lookup shows.
func answerOf(rr dnsmessage.Resource) (Answer, bool) {
	ttl := rr.Header.TTL
	a := Answer{TTL: &ttl}
	switch b := rr.Body.(type) {
	case *dnsmessage.AResource:
		a.Type, a.Value = "A", netip.AddrFrom4(b.A).String()
	case *dnsmessage.AAAAResource:
		a.Type, a.Value = "AAAA", netip.AddrFrom16(b.AAAA).String()
	case *dnsmessage.CNAMEResource:
		a.Type, a.Value = "CNAME", strings.TrimSuffix(b.CNAME.String(), ".")
	case *dnsmessage.MXResource:
		a.Type, a.Value = "MX", fmt.Sprintf("%d %s", b.Pref, strings.TrimSuffix(b.MX.String(), "."))
	case *dnsmessage.TXTResource:
		a.Type, a.Value = "TXT", strings.Join(b.TXT, "")
	case *dnsmessage.PTRResource:
		a.Type, a.Value = "PTR", strings.TrimSuffix(b.PTR.String(), ".")
	default:
		return a, false
	}
	return a, true
}

// systemLookup is what the system resolver (the one the core's own checks use) answers, for the
// types it can be asked: addresses, and names for an address. Other types have no system answer.
func systemLookup(ctx context.Context, r Request) []Answer {
	lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out := []Answer{}
	switch r.Type {
	case "both", "A", "AAAA":
		nets := map[string]string{"A": "ip4", "AAAA": "ip6"}
		types := []string{"A", "AAAA"}
		if r.Type != "both" {
			types = []string{r.Type}
		}
		for _, t := range types {
			ips, err := net.DefaultResolver.LookupNetIP(lctx, nets[t], r.Target)
			if err != nil || len(ips) == 0 {
				out = append(out, Answer{Type: t, Error: lookupWhy(err)})
				continue
			}
			for _, ip := range ips {
				out = append(out, Answer{Type: t, Value: ip.Unmap().String()})
			}
		}
	case "PTR":
		names, err := net.DefaultResolver.LookupAddr(lctx, r.Target)
		if err != nil || len(names) == 0 {
			return append(out, Answer{Type: "PTR", Error: lookupWhy(err)})
		}
		for _, n := range names {
			out = append(out, Answer{Type: "PTR", Value: strings.TrimSuffix(n, ".")})
		}
	}
	return out
}

// differ reports whether the system resolver's answers disagree with the server's, type by type:
// different values, or one side has values where the other has none.
func differ(server, system []Answer) bool {
	vals := func(as []Answer, t string) []string {
		v := []string{}
		for _, a := range as {
			if a.Type == t && a.Error == "" {
				v = append(v, a.Value)
			}
		}
		slices.Sort(v)
		return v
	}
	seen := map[string]bool{}
	for _, a := range system {
		if seen[a.Type] {
			continue
		}
		seen[a.Type] = true
		if !slices.Equal(vals(server, a.Type), vals(system, a.Type)) {
			return true
		}
	}
	return false
}

// reverseZone is an address's PTR name (4.3.2.1.in-addr.arpa).
func reverseZone(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", b[3], b[2], b[1], b[0])
	}
	b := a.As16()
	var s strings.Builder
	for i := len(b) - 1; i >= 0; i-- {
		fmt.Fprintf(&s, "%x.%x.", b[i]&0xf, b[i]>>4)
	}
	return s.String() + "ip6.arpa"
}

func typeName(t dnsmessage.Type) string {
	return strings.TrimPrefix(t.String(), "Type")
}

func rcodeName(c dnsmessage.RCode) string {
	switch c {
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	}
	return strings.TrimPrefix(c.String(), "RCode")
}

// rcodeWhy is a failed answer in plain words.
func rcodeWhy(c dnsmessage.RCode) string {
	switch c {
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN: the name does not exist"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL: the server could not get an answer"
	case dnsmessage.RCodeRefused:
		return "REFUSED: the server would not answer"
	}
	return rcodeName(c)
}
