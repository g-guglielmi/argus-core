// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

//go:build !linux

package diag

import "context"

// The unprivileged datagram ICMP socket is Linux-only (where Argus actually runs); local dev builds
// elsewhere say so instead of pinging.
const icmpUnsupported = "ping and traceroute from the core need Linux"

func runPing(context.Context, Request, func(Result)) Result {
	return Result{Error: icmpUnsupported, Ping: &Ping{Replies: []Reply{}}}
}

func runTrace(context.Context, Request, func(Result)) Result {
	return Result{Error: icmpUnsupported, Trace: &Trace{Hops: []Hop{}}}
}
