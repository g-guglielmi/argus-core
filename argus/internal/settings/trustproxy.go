// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package settings

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// TrustProxy says which reverse proxies Argus believes about the client (X-Forwarded-For) and the
// requested host and scheme (X-Forwarded-Host / -Proto). The setting (ARGUS_TRUST_PROXY) is one of:
//
//   - empty / "false": no proxy; headers are ignored and the socket address is the client;
//   - "true": exactly one proxy in front of Argus, whoever connects; the client is the last
//     X-Forwarded-For entry (the one that proxy appended);
//   - a list of proxy networks ("10.0.0.2, 10.0.5.0/24"): headers count only on a connection from
//     one of them, and the client is found by reading X-Forwarded-For from the right, skipping every
//     address that belongs to a listed proxy - so a chain like NetScaler -> HAProxy -> Argus works.
//
// Entries left of the first untrusted address came from the client and are never believed.
type TrustProxy struct {
	Enabled bool
	Nets    []netip.Prefix // empty with Enabled = trust one hop from anyone
}

// ParseTrustProxy reads the setting and returns it with its canonical text ("" off, "true", or the
// normalized network list).
func ParseTrustProxy(v string) (TrustProxy, string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "no", "off", "0":
		return TrustProxy{}, "", nil
	case "true", "yes", "on", "1":
		return TrustProxy{Enabled: true}, "true", nil
	}
	var nets []netip.Prefix
	var text []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return TrustProxy{}, "", fmt.Errorf("trusted proxies: %q isn't true, false, an address or a network (like 10.0.0.0/24)", f)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		p = p.Masked()
		nets = append(nets, p)
		text = append(text, p.String())
	}
	return TrustProxy{Enabled: true, Nets: nets}, strings.Join(text, ", "), nil
}

func (t TrustProxy) inNets(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t.Nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// peerAddr is the socket address of the connection (the proxy, when there is one).
func peerAddr(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// Trusts reports whether a connection from remoteAddr may speak for the client (its forwarded
// headers count).
func (t TrustProxy) Trusts(remoteAddr string) bool {
	if !t.Enabled {
		return false
	}
	if len(t.Nets) == 0 {
		return true
	}
	a, err := netip.ParseAddr(peerAddr(remoteAddr))
	return err == nil && t.inNets(a)
}

// ClientIP is the real client's address for a request that arrived from remoteAddr carrying these
// X-Forwarded-For header values (every copy of the header, in order).
func (t TrustProxy) ClientIP(remoteAddr string, xff []string) string {
	peer := peerAddr(remoteAddr)
	if !t.Trusts(remoteAddr) {
		return peer
	}
	var chain []string
	for _, v := range xff {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				chain = append(chain, p)
			}
		}
	}
	if len(chain) == 0 {
		return peer
	}
	if len(t.Nets) == 0 { // one proxy: the address it appended
		return chain[len(chain)-1]
	}
	for i := len(chain) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(chain[i])
		if err != nil || !t.inNets(a) {
			return chain[i] // the first address that isn't one of our proxies
		}
	}
	return chain[0] // every hop was a trusted proxy: the earliest is as close to the client as we get
}
