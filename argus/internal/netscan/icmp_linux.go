// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

//go:build linux

package netscan

import (
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// pingProbe sends one ICMP echo via an UNPRIVILEGED datagram ICMP socket - no raw socket, no
// CAP_NET_RAW, works as the container's nonroot user whenever the runtime allows it
// (net.ipv4.ping_group_range; Docker opens it for containers by default). supported=false means
// the kernel refused the socket - the caller then falls back to port/SNMP-based liveness, so an
// ICMP-only device is simply not seen on such runtimes (documented in the package comment).
func pingProbe(addr netip.Addr, timeout time.Duration) (alive, supported bool) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMP)
	if err != nil {
		return false, false
	}
	defer unix.Close(fd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	// Echo request: type 8, code 0, checksum, id (the kernel rewrites it to the socket's own),
	// seq 1, tiny payload.
	pkt := []byte{8, 0, 0, 0, 0, 0, 0, 1, 'a', 'r', 'g', 'u', 's'}
	cs := icmpChecksum(pkt)
	pkt[2], pkt[3] = byte(cs>>8), byte(cs&0xff)
	if err := unix.Sendto(fd, pkt, 0, &unix.SockaddrInet4{Addr: addr.As4()}); err != nil {
		return false, true
	}
	// The datagram ICMP socket only delivers replies matching our id, without the IP header.
	buf := make([]byte, 1500)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return false, true // timeout (SO_RCVTIMEO) or transient error
		}
		if n >= 1 && buf[0] == 0 { // echo reply
			return true, true
		}
	}
	return false, true
}

// icmpChecksum is the standard ones'-complement 16-bit checksum (checksum field zeroed).
func icmpChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
