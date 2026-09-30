// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package server

import (
	"context"
	"strings"

	"argus/internal/zabbix"
)

// Why a sensor isn't reading. Two shapes, both shown next to the reading (a hover in the UI) and put
// in the alert:
//   - a "not supported" sensor carries Zabbix's own error, which for an Argus script template is the
//     message the script threw ("UniFi API HTTP 400 (api.err.NoSiteContext) - ...");
//   - a collector that reports its target down on purpose (so the down trigger fires instead of every
//     sensor going unsupported) prints why in its JSON, and its template keeps that in a
//     "<prefix>.error" item next to the down flag.
//
// Every Argus template follows this; a new collector adds its flag here. DESIGN section 5.

// reasonKeys maps a collector's up/down flag to the item that says why it reads down.
var reasonKeys = map[string]string{
	"linux.ssh.reachable": "linux.ssh.error",
	"xcp.reachable":       "xcp.error",
	"xcp.authed":          "xcp.error",
	"nut.reachable":       "nut.error",
	"adguard.running":     "adguard.error",
	"hass.running":        "hass.error",
	"dns.resolve.success": "dns.resolve.error",
	// Linux by SSH, per systemd unit and per Docker container: the state text is the reason.
	"linux.ssh.unit.active":       "linux.ssh.unit.state",
	"linux.ssh.container.running": "linux.ssh.container.status",
}

// reasonKeyFor is the key of the item holding the reason for flag key (same parameters: a DNS
// name's reason sits next to its own success flag), "" when key has none.
func reasonKeyFor(key string) string {
	base, params := key, ""
	if i := strings.IndexByte(key, '['); i >= 0 {
		base, params = key[:i], key[i:]
	}
	r, ok := reasonKeys[base]
	if !ok {
		return ""
	}
	return r + params
}

// isReasonKey reports whether key is one of the reason items (their values are looked up by host).
func isReasonKey(key string) bool {
	base := key
	if i := strings.IndexByte(key, '['); i >= 0 {
		base = key[:i]
	}
	for _, r := range reasonKeys {
		if base == r {
			return true
		}
	}
	return false
}

// flagDown reports whether a collector flag's reading is "down" (0).
func flagDown(value string) bool { return strings.TrimSpace(value) == "0" }

// itemError trims Zabbix's error text for display: its first line, without the "Cannot execute
// script: " wrapper a script item's own message comes in, at most 200 characters.
func itemError(msg string) string {
	msg = strings.TrimSpace(msg)
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "Cannot execute script:"))
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "Error:"))
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}

// reasonIndex holds the reason items' last values, by host and key.
type reasonIndex map[string]string

func (ri reasonIndex) add(hostID, key, value string) {
	if v := strings.TrimSpace(value); v != "" && isReasonKey(key) {
		ri[hostID+"\x00"+key] = itemError(v)
	}
}

// why is the reason a sensor shows: Zabbix's error while it's not supported, else the collector's
// reason while its flag reads down, else "".
func (ri reasonIndex) why(hostID, key, value, zbxError string, supported bool) string {
	if !supported {
		return itemError(zbxError)
	}
	if rk := reasonKeyFor(key); rk != "" && flagDown(value) {
		return ri[hostID+"\x00"+rk]
	}
	return ""
}

// collectorReason fetches the reason behind a collector flag that reads down, for the alert. "" for
// any other sensor, a flag that reads up, or a collector that gave no reason.
func collectorReason(ctx context.Context, zbx *zabbix.Client, hostID, key, value string) string {
	rk := reasonKeyFor(key)
	if rk == "" || !flagDown(value) || hostID == "" {
		return ""
	}
	v, err := zbx.HostItemLastValue(ctx, hostID, rk)
	if err != nil {
		return ""
	}
	return itemError(v)
}

// runningKeys are the per-service flags (1 running, 0 down) that read as words.
var runningKeys = map[string]bool{"linux.ssh.unit.active": true, "linux.ssh.container.running": true}

// runningReading words a service or container flag: "Running" or "Down" (its state item says why).
func runningReading(key, value string) (string, bool) {
	base := key
	if i := strings.IndexByte(key, '['); i >= 0 {
		base = key[:i]
	}
	if !runningKeys[base] {
		return "", false
	}
	switch strings.TrimSpace(value) {
	case "1":
		return "Running", true
	case "0":
		return "Down", true
	}
	return "", false
}

// withReason words a reading with its reason: "Not reachable: Permission denied (publickey)", or the
// reason alone when the reading is a bare flag value.
func withReason(value, reason string) string {
	switch {
	case reason == "":
		return value
	case value == "" || value == "0" || value == "1":
		return reason
	}
	return value + ": " + reason
}
