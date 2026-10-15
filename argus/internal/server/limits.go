// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"hash/fnv"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// limiterAddr is the client address as the limiters key it: an IPv4 address as is, an IPv6
// address by its /64. A residential IPv6 connection hands out a whole /64, so keying on the full
// address would give one client 2^64 fresh limiter keys.
func limiterAddr(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// limiterIP is limiterAddr of the request's client address.
func (s *Server) limiterIP(r *http.Request) string { return limiterAddr(s.clientIP(r)) }

// peerIP is the address the connection itself came from, whatever any forwarded header says.
func peerIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// acctDelay is how long an attempt on an over-limit account waits before its password is checked.
const acctDelay = 3 * time.Second

// acctStripes serialises attempts on an over-limit account: a keyed lock over a fixed number of
// mutexes (a collision only makes two unrelated accounts queue briefly), so the map never grows
// with the names an attacker sends.
type acctStripes struct {
	mu [64]sync.Mutex
}

// slow makes an attempt on an over-limit account wait its turn and acctDelay before it goes on:
// a person signs in a little later, a guesser gets a fraction of a guess per second whatever
// addresses it comes from. Returns false when the request went away while waiting.
func (a *acctStripes) slow(r *http.Request, key string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	m := &a.mu[h.Sum32()%uint32(len(a.mu))]
	m.Lock()
	defer m.Unlock()
	select {
	case <-time.After(acctDelay):
		return true
	case <-r.Context().Done():
		return false
	}
}
