// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

//go:build !linux

package netscan

import (
	"net/netip"
	"time"
)

// pingProbe: the unprivileged datagram ICMP socket is Linux-only (where Argus actually runs);
// elsewhere (local dev builds) liveness falls back to port/SNMP answers.
func pingProbe(netip.Addr, time.Duration) (alive, supported bool) {
	return false, false
}
