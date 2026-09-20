// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package netscan

import (
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minimal BER encode/decode, enough for an SNMP v1/v2c GET of the system group - the Go twin of
// the probe scanner's hand-rolled codec (argus_netscan.py).

var snmpOIDs = []struct{ name, oid string }{
	{"sysdescr", "1.3.6.1.2.1.1.1.0"},
	{"sysobjectid", "1.3.6.1.2.1.1.2.0"},
	{"sysname", "1.3.6.1.2.1.1.5.0"},
}

func berTLV(tag byte, payload []byte) []byte {
	n := len(payload)
	if n < 128 {
		return append([]byte{tag, byte(n)}, payload...)
	}
	var lb []byte
	for v := n; v > 0; v >>= 8 {
		lb = append([]byte{byte(v & 0xff)}, lb...)
	}
	out := append([]byte{tag, 0x80 | byte(len(lb))}, lb...)
	return append(out, payload...)
}

func berInt(v int64) []byte {
	if v == 0 {
		return berTLV(0x02, []byte{0})
	}
	var b []byte
	for x := v; x > 0; x >>= 8 {
		b = append([]byte{byte(x & 0xff)}, b...)
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...) // keep the sign bit clear
	}
	return berTLV(0x02, b)
}

func berOID(oid string) []byte {
	parts := strings.Split(strings.Trim(oid, "."), ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		nums = append(nums, n)
	}
	if len(nums) < 2 {
		return berTLV(0x06, nil)
	}
	body := []byte{byte(nums[0]*40 + nums[1])}
	for _, p := range nums[2:] {
		chunk := []byte{byte(p & 0x7f)}
		for p >>= 7; p > 0; p >>= 7 {
			chunk = append([]byte{0x80 | byte(p&0x7f)}, chunk...)
		}
		body = append(body, chunk...)
	}
	return berTLV(0x06, body)
}

type berNode struct {
	tag  byte
	val  []byte    // primitive payload
	kids []berNode // constructed children
}

// berDecode parses one TLV at data[i]; constructed tags (bit 0x20) decode into kids.
func berDecode(data []byte, i int) (berNode, int, error) {
	if i+2 > len(data) {
		return berNode{}, 0, fmt.Errorf("truncated TLV header")
	}
	tag := data[i]
	ln := int(data[i+1])
	i += 2
	if ln&0x80 != 0 {
		n := ln & 0x7f
		if n == 0 || n > 4 || i+n > len(data) {
			return berNode{}, 0, fmt.Errorf("bad TLV length")
		}
		ln = 0
		for _, c := range data[i : i+n] {
			ln = ln<<8 | int(c)
		}
		i += n
	}
	end := i + ln
	if end > len(data) {
		return berNode{}, 0, fmt.Errorf("TLV overruns buffer")
	}
	if tag&0x20 != 0 {
		node := berNode{tag: tag}
		for i < end {
			kid, next, err := berDecode(data, i)
			if err != nil {
				return berNode{}, 0, err
			}
			node.kids = append(node.kids, kid)
			i = next
		}
		return node, end, nil
	}
	return berNode{tag: tag, val: data[i:end]}, end, nil
}

func oidToStr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	parts := []string{fmt.Sprint(int(b[0]) / 40), fmt.Sprint(int(b[0]) % 40)}
	val := 0
	for _, c := range b[1:] {
		val = val<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			parts = append(parts, fmt.Sprint(val))
			val = 0
		}
	}
	return "." + strings.Join(parts, ".")
}

// buildSNMPGet encodes one GetRequest for the three system OIDs.
func buildSNMPGet(cred *SNMPCred, reqID int64) []byte {
	var varbinds []byte
	for _, o := range snmpOIDs {
		varbinds = append(varbinds, berTLV(0x30, append(berOID(o.oid), berTLV(0x05, nil)...))...)
	}
	pdu := berTLV(0xA0, concat(berInt(reqID), berInt(0), berInt(0), berTLV(0x30, varbinds)))
	version := int64(1) // v2c
	if cred.Version == 1 {
		version = 0
	}
	return berTLV(0x30, concat(berInt(version), berTLV(0x04, []byte(cred.Community)), pdu))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// parseSNMPResponse extracts the system-group values from a GetResponse. ok reports that the
// message parsed as an SNMP response at all - an answer is itself a fingerprint.
func parseSNMPResponse(data []byte) (info SNMPInfo, ok bool) {
	root, _, err := berDecode(data, 0)
	if err != nil || root.tag != 0x30 {
		return SNMPInfo{}, false
	}
	var pdu *berNode
	for i := range root.kids {
		if root.kids[i].tag >= 0xA0 {
			pdu = &root.kids[i]
		}
	}
	if pdu == nil || len(pdu.kids) < 4 {
		return SNMPInfo{}, false
	}
	byOID := map[string]*string{}
	for i, o := range snmpOIDs {
		switch i {
		case 0:
			byOID[o.oid] = &info.SysDescr
		case 1:
			byOID[o.oid] = &info.SysObjectID
		case 2:
			byOID[o.oid] = &info.SysName
		}
	}
	for _, vb := range pdu.kids[3].kids {
		if len(vb.kids) < 2 {
			continue
		}
		oid := strings.TrimPrefix(oidToStr(vb.kids[0].val), ".")
		dst := byOID[oid]
		if dst == nil {
			continue
		}
		v := vb.kids[1]
		switch v.tag {
		case 0x04:
			*dst = strings.TrimSpace(string(v.val))
		case 0x06:
			*dst = oidToStr(v.val)
		}
	}
	return info, true
}

// snmpGet fetches the system group; nil when the agent never answered (v1/v2c agents stay silent
// on a wrong community, so an answer at all is a fingerprint - even with empty values).
func snmpGet(ip string, cred *SNMPCred) *SNMPInfo {
	port := cred.Port
	if port <= 0 || port > 65535 {
		port = 161
	}
	msg := buildSNMPGet(cred, int64(rand.Intn(0x7ffffff)+1))
	for try := 0; try < 2; try++ { // UDP: one retry
		conn, err := net.DialTimeout("udp", net.JoinHostPort(ip, fmt.Sprint(port)), 1500*time.Millisecond)
		if err != nil {
			return nil
		}
		_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
		_, _ = conn.Write(msg)
		buf := make([]byte, 65535)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err != nil || n == 0 {
			continue
		}
		if info, ok := parseSNMPResponse(buf[:n]); ok {
			return &info
		}
	}
	return nil
}
