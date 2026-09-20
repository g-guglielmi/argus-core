// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package netscan

import (
	"net/netip"
	"testing"
)

// The GetRequest we encode must decode back into the exact structure parseSNMPResponse expects
// (same codec both ways), and a synthetic GetResponse must yield the system-group values.
func TestBERCodecRoundTrip(t *testing.T) {
	cred := &SNMPCred{Version: 2, Community: "public"}
	msg := buildSNMPGet(cred, 1234)
	root, n, err := berDecode(msg, 0)
	if err != nil || n != len(msg) || root.tag != 0x30 {
		t.Fatalf("decode: tag=%x n=%d len=%d err=%v", root.tag, n, len(msg), err)
	}
	if len(root.kids) != 3 || string(root.kids[1].val) != "public" {
		t.Fatalf("message shape: %+v", root)
	}
	pdu := root.kids[2]
	if pdu.tag != 0xA0 || len(pdu.kids) != 4 {
		t.Fatalf("pdu shape: tag=%x kids=%d", pdu.tag, len(pdu.kids))
	}
	oids := []string{}
	for _, vb := range pdu.kids[3].kids {
		oids = append(oids, oidToStr(vb.kids[0].val))
	}
	want := []string{".1.3.6.1.2.1.1.1.0", ".1.3.6.1.2.1.1.2.0", ".1.3.6.1.2.1.1.5.0"}
	for i, w := range want {
		if oids[i] != w {
			t.Errorf("oid %d = %q, want %q", i, oids[i], w)
		}
	}

	// Build a GetResponse (tag 0xA2) carrying values and parse it.
	vb := func(oid string, valTLV []byte) []byte {
		return berTLV(0x30, append(berOID(oid), valTLV...))
	}
	varbinds := concat(
		vb("1.3.6.1.2.1.1.1.0", berTLV(0x04, []byte("Linux storage1 6.1.0"))),
		vb("1.3.6.1.2.1.1.2.0", berOID("1.3.6.1.4.1.8072.3.2.10")),
		vb("1.3.6.1.2.1.1.5.0", berTLV(0x04, []byte("storage1"))),
	)
	respPDU := berTLV(0xA2, concat(berInt(1234), berInt(0), berInt(0), berTLV(0x30, varbinds)))
	resp := berTLV(0x30, concat(berInt(1), berTLV(0x04, []byte("public")), respPDU))
	info, ok := parseSNMPResponse(resp)
	if !ok {
		t.Fatal("response did not parse")
	}
	if info.SysDescr != "Linux storage1 6.1.0" || info.SysName != "storage1" ||
		info.SysObjectID != ".1.3.6.1.4.1.8072.3.2.10" {
		t.Fatalf("parsed values: %+v", info)
	}
	// Garbage must not parse as an answer.
	if _, ok := parseSNMPResponse([]byte{0x30, 0x82}); ok {
		t.Error("truncated garbage parsed as a response")
	}
}

func TestBERIntEdges(t *testing.T) {
	for _, v := range []int64{0, 1, 127, 128, 255, 256, 1234567} {
		node, _, err := berDecode(berInt(v), 0)
		if err != nil || node.tag != 0x02 {
			t.Fatalf("v=%d: %v", v, err)
		}
		got := int64(0)
		for _, b := range node.val {
			got = got<<8 | int64(b)
		}
		if got != v {
			t.Errorf("berInt(%d) round-tripped to %d", v, got)
		}
	}
}

func TestHostsInPrefix(t *testing.T) {
	p := netip.MustParsePrefix("10.0.0.0/30")
	hs := hostsInPrefix(p, MaxHosts)
	if len(hs) != 2 || hs[0].String() != "10.0.0.1" || hs[1].String() != "10.0.0.2" {
		t.Fatalf("/30 hosts = %v (network/broadcast must be excluded)", hs)
	}
	if hs := hostsInPrefix(netip.MustParsePrefix("10.0.0.5/32"), MaxHosts); len(hs) != 1 || hs[0].String() != "10.0.0.5" {
		t.Fatalf("/32 hosts = %v", hs)
	}
	if hs := hostsInPrefix(netip.MustParsePrefix("10.0.0.0/31"), MaxHosts); len(hs) != 2 {
		t.Fatalf("/31 hosts = %v (point-to-point keeps both)", hs)
	}
	if hs := hostsInPrefix(netip.MustParsePrefix("10.0.0.0/22"), MaxHosts); len(hs) != 1022 {
		t.Fatalf("/22 = %d hosts, want 1022 (1024 minus network + broadcast)", len(hs))
	}
	if hs := hostsInPrefix(netip.MustParsePrefix("10.0.0.0/21"), MaxHosts); len(hs) != MaxHosts {
		t.Fatalf("oversized prefix = %d hosts, want the %d cap", len(hs), MaxHosts)
	}
}
