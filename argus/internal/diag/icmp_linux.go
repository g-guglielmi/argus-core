// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

//go:build linux

package diag

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// icmpSocket is an unprivileged datagram ICMP socket: the kernel fills in our id and hands back only
// the replies that carry it, without the IP header. IP_RECVERR queues the ICMP errors our echoes
// cause (a router's "time exceeded", a "destination unreachable") for the traceroute.
func icmpSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.IPPROTO_ICMP)
	if err != nil {
		return -1, errors.New("this host does not let the core send pings (net.ipv4.ping_group_range)")
	}
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// sendEcho sends echo request seq to addr.
func sendEcho(fd int, addr netip.Addr, seq int) error {
	pkt := make([]byte, 8+56)
	pkt[0] = 8 // echo request; code, checksum and id stay 0 (the kernel sets the id)
	binary.BigEndian.PutUint16(pkt[6:], uint16(seq))
	copy(pkt[8:], "argus diagnostics")
	cs := checksum(pkt)
	binary.BigEndian.PutUint16(pkt[2:], cs)
	return unix.Sendto(fd, pkt, 0, &unix.SockaddrInet4{Addr: addr.As4()})
}

// answer is what came back for an echo: the target's reply, or a router's error about it.
type answer struct {
	from    netip.Addr
	reply   bool  // the target answered
	icmp    uint8 // the error's ICMP type (11 time exceeded, 3 destination unreachable)
	code    uint8
	elapsed time.Duration
}

// await waits up to timeout for the answer to echo seq, sent at sent. Answers to earlier echoes
// (a late reply) are passed over.
func await(ctx context.Context, fd int, seq int, sent time.Time, timeout time.Duration) (answer, bool) {
	deadline := sent.Add(timeout)
	buf := make([]byte, 1500)
	oob := make([]byte, 512)
	for {
		left := time.Until(deadline)
		if left <= 0 || ctx.Err() != nil {
			return answer{}, false
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLERR}}
		n, err := unix.Poll(fds, int(min(left, 200*time.Millisecond).Milliseconds())+1)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return answer{}, false
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLERR != 0 {
			// The error queue: the original echo comes back as data, the error and the router that
			// sent it as an IP_RECVERR control message.
			dn, on, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
			if err == nil && dn >= 8 && int(binary.BigEndian.Uint16(buf[6:])) == seq {
				if a, ok := parseErr(oob[:on]); ok {
					a.elapsed = time.Since(sent)
					return a, true
				}
			}
			continue
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			rn, from, err := unix.Recvfrom(fd, buf, unix.MSG_DONTWAIT)
			if err != nil || rn < 8 || buf[0] != 0 || int(binary.BigEndian.Uint16(buf[6:])) != seq {
				continue
			}
			a := answer{reply: true, elapsed: time.Since(sent)}
			if sa, ok := from.(*unix.SockaddrInet4); ok {
				a.from = netip.AddrFrom4(sa.Addr)
			}
			return a, true
		}
	}
}

// parseErr reads the IP_RECVERR message: struct sock_extended_err, then the offender's sockaddr_in.
func parseErr(oob []byte) (answer, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return answer{}, false
	}
	for _, m := range msgs {
		if m.Header.Level != unix.IPPROTO_IP || m.Header.Type != unix.IP_RECVERR || len(m.Data) < 16+8 {
			continue
		}
		if m.Data[4] != unix.SO_EE_ORIGIN_ICMP { // ee_origin
			continue
		}
		var a answer
		a.icmp, a.code = m.Data[5], m.Data[6]
		if binary.NativeEndian.Uint16(m.Data[16:]) == unix.AF_INET {
			a.from = netip.AddrFrom4([4]byte(m.Data[20:24]))
		}
		return a, true
	}
	return answer{}, false
}

func runPing(ctx context.Context, r Request, progress func(Result)) Result {
	res := Result{Ping: &Ping{Replies: []Reply{}}}
	addr, err := resolve4(ctx, r.Target)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Addr = addr.String()
	fd, err := icmpSocket()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer unix.Close(fd)
	for seq := 0; seq < r.Count && ctx.Err() == nil; seq++ {
		start := time.Now()
		rep := Reply{Seq: seq}
		if sendEcho(fd, addr, seq) == nil {
			if a, ok := await(ctx, fd, seq, start, time.Second); ok && a.reply {
				rep.From, rep.MS = a.from.String(), ms(a.elapsed)
			}
		}
		res.Ping.Replies = append(res.Ping.Replies, rep)
		res.Raw = pingRaw(r.Target, addr, res.Ping, false)
		progress(res)
		if seq < r.Count-1 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Until(start.Add(time.Second))):
			}
		}
	}
	res.Raw = pingRaw(r.Target, addr, res.Ping, true)
	return res
}

func runTrace(ctx context.Context, r Request, progress func(Result)) Result {
	res := Result{Trace: &Trace{Hops: []Hop{}}}
	addr, err := resolve4(ctx, r.Target)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Addr = addr.String()
	fd, err := icmpSocket()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer unix.Close(fd)
	for ttl := 1; ttl <= MaxHops && ctx.Err() == nil; ttl++ {
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, ttl); err != nil {
			res.Error = "could not set the hop limit: " + err.Error()
			break
		}
		start := time.Now()
		hop := Hop{N: ttl}
		stop := false
		if sendEcho(fd, addr, ttl) == nil {
			if a, ok := await(ctx, fd, ttl, start, 2*time.Second); ok {
				if a.from.IsValid() {
					hop.Addr = a.from.String()
					hop.Name = reverseName(ctx, hop.Addr)
				}
				hop.MS = ms(a.elapsed)
				switch {
				case a.reply:
					res.Trace.Reached, stop = true, true
				case a.icmp == 3: // destination unreachable: the path ends here
					hop.Note, stop = unreachableWhy(a.code), true
				}
			}
		}
		res.Trace.Hops = append(res.Trace.Hops, hop)
		res.Raw = traceRaw(r.Target, addr, res.Trace)
		progress(res)
		if stop {
			break
		}
	}
	return res
}

// unreachableWhy is a "destination unreachable" code in plain words.
func unreachableWhy(code uint8) string {
	switch code {
	case 0:
		return "network unreachable"
	case 1:
		return "host unreachable"
	case 9, 10, 13:
		return "blocked by a filter"
	}
	return "unreachable"
}

// checksum is the standard ones'-complement 16-bit checksum (the checksum field zeroed).
func checksum(b []byte) uint16 {
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
