package server

import "strings"

// Curation maps raw Zabbix item keys to a small set of sensor categories the user cares
// about (ping, CPU, memory, disk, …) with friendly labels. Items that don't match a rule
// are considered "noise" and hidden unless the caller asks for the full list.
//
// Matching is by the item key's base (the part before the first "[") plus its parameters,
// which keeps multi-instance sensors (per-mount disk, per-interface network) distinct.

// cpuUtilKeep is the set of CPU-utilization states shown in the curated view ("" = the
// overall/aggregate item). Other states (nice/interrupt/softirq/guest/…) go to "All sensors".
var cpuUtilKeep = map[string]bool{
	"":       true,
	"user":   true,
	"system": true,
	"iowait": true,
	"idle":   true,
	"steal":  true,
}

// Category order is per host SHAPE (user call): network gear - anything with switch ports - reads
// network-first, while servers keep the classic compute-first order. Picking the order from the UI
// (per class or per host) is planned for the management UI (ROADMAP §D).
var categoryOrderServer = map[string]int{
	"Ping":        0,
	"Web":         1,
	"Power":       2, // before CPU (user call) - matters for power-centric classes (PoE switches, UPS)
	"CPU":         3,
	"Memory":      4,
	"Disk":        5,
	"Network":     6,
	"Wireless":    7,
	"Services":    8,
	"Temperature": 9,
	"Uptime":      10,
	"Ports":       11,
	"Status":      12,
}
var categoryOrderNet = map[string]int{
	"Ping":        0,
	"Web":         1,
	"Wireless":    2, // an AP's headline is its clients/radios
	"Network":     3,
	"Power":       4, // before CPU (user call)
	"CPU":         5,
	"Memory":      6,
	"Disk":        7,
	"Services":    8,
	"Uptime":      9,
	"Ports":       10,
	"Temperature": 11,
	"Status":      12,
}

// Storage boxes (anything with a drive-temperature group: unRAID, later QNAP/Ugreen) read their
// drive temperatures right before the Disk section (user call) - the drives ARE the machine.
var categoryOrderNAS = map[string]int{
	"Ping":        0,
	"Web":         1,
	"Power":       2,
	"CPU":         3,
	"Memory":      4,
	"Temperature": 5, // before Disk (user call)
	"Disk":        6,
	"Network":     7,
	"Wireless":    8,
	"Services":    9,
	"Uptime":      10,
	"Ports":       11,
	"Status":      12,
}

// splitKey returns the base key and its parameters, e.g. vfs.fs.size[/,pused] ->
// ("vfs.fs.size", ["/", "pused"]).
func splitKey(key string) (string, []string) {
	i := strings.IndexByte(key, '[')
	if i < 0 {
		return key, nil
	}
	base := key[:i]
	inner := strings.TrimSuffix(key[i+1:], "]")
	parts := strings.Split(inner, ",")
	for j := range parts {
		parts[j] = strings.Trim(strings.TrimSpace(parts[j]), `"`)
	}
	return base, parts
}

func param(params []string, i int) string {
	if i < len(params) {
		return params[i]
	}
	return ""
}

// hideWhenZero marks sensors that are capability placeholders when they read 0 or have never
// delivered a value (a device without a temperature probe or without PoE reports a constant 0; a
// gateway that never ran a speedtest reports nothing) - the curated view drops such rows. Keyed by
// the item key's BASE, so per-instance keys (unifi.wan.latency[1]) are covered too.
var hideWhenZero = map[string]bool{
	"unifi.temp":           true,
	"unifi.poe.total":      true,
	"unifi.experience":     true,
	"unifi.speedtest.down": true,
	"unifi.speedtest.up":   true,
	"unifi.wan.latency":    true, // a real ping is never 0; absent monitor data leaves a stale row
}

// hideZero reports whether this item key is a capability placeholder when it reads 0.
func hideZero(key string) bool {
	base, _ := splitKey(key)
	return hideWhenZero[base]
}

// naturalLess compares labels with embedded numbers numerically, so "Port 2" sorts before
// "Port 10" (and eth2 before eth10). Digit runs compare as integers; everything else bytewise.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			si, sj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parenSuffix returns the content of a trailing "(...)" in an item name, e.g.
// "Port 3 link (Shield-TV)" -> "Shield-TV"; "" when there is none.
func parenSuffix(name string) string {
	if !strings.HasSuffix(name, ")") {
		return ""
	}
	i := strings.LastIndexByte(name, '(')
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(name[i+1 : len(name)-1])
}

// wanInstance names a gateway WAN group from its index: the first link is just "WAN" (most
// gateways have one), a second becomes "WAN 2".
func wanInstance(idx string) string {
	if idx == "" || idx == "1" {
		return "WAN"
	}
	return "WAN " + idx
}

// trafficLabel builds a network-traffic label, distinguishing the byte-rate item from the
// per-interface error/dropped/packet counters that share the net.if.in/out key. iface is the
// display name; mode (param 1) "" or "bytes" is the main rate.
func trafficLabel(base, iface string, p []string) string {
	label := base
	if mode := param(p, 1); mode != "" && mode != "bytes" {
		label += " " + mode
	}
	if iface != "" {
		label += " (" + iface + ")"
	}
	return label
}

// ifaceName is the display name for a network interface: the friendly name a template lifts into the
// item name ("Traffic in (Ethernet 2)" - Windows uses ifAlias) when present, else the key parameter
// (the ifName/ifIndex used to key the item). Linux items name themselves by ifName, so the paren
// content equals the key param and nothing changes.
func ifaceName(name string, p []string) string {
	if s := parenSuffix(name); s != "" {
		return s
	}
	return param(p, 0)
}

// chanMode names a network channel (In/Out) including a non-byte mode (errors, dropped, …).
func chanMode(dir string, p []string) string {
	if mode := param(p, 1); mode != "" && mode != "bytes" {
		return dir + " " + mode
	}
	return dir
}

// classifyItem returns (category, label, instance, channel, matched) for a Zabbix item key/name.
// instance groups the per-target sensors of one thing - a disk mount, a network interface - so the
// UI can stack them PRTG-style; channel is the metric within that instance. Sensors that aren't part
// of a multi-instance set return instance "" (and render individually). label is the full flat name
// (kept for the "All sensors" / overview lists), e.g. "Disk used % (/)".
func classifyItem(key, name string) (category, label, instance, channel string, matched bool) {
	base, p := splitKey(key)

	switch base {
	case "icmpping":
		return "Ping", "Reachable (ICMP)", "ICMP", "Reachable", true
	case "icmppingloss":
		return "Ping", "ICMP loss", "ICMP", "Loss", true
	case "icmppingsec":
		return "Ping", "ICMP response time", "ICMP", "Response time", true

	case "system.cpu.util":
		// The Linux template has one item per CPU state, most of them near-zero noise. Keep
		// only the meaningful states in the curated view; the rest fall under "All sensors".
		state := param(p, 1)
		if !cpuUtilKeep[state] {
			return "", "", "", "", false
		}
		if state != "" {
			return "CPU", "CPU utilization (" + state + ")", "", "", true
		}
		return "CPU", "CPU utilization", "", "", true
	case "system.cpu.load":
		if a := param(p, 1); a != "" {
			return "CPU", "CPU load (" + a + ")", "", "", true
		}
		return "CPU", "CPU load", "", "", true
	case "system.cpu.core":
		// Per-core loads (hrProcessorLoad LLD) group into one "Cores" row; the frontend headlines
		// the busiest member, PRTG-style.
		return "CPU", name, "Cores", "Core " + param(p, 0), true

	case "vm.memory.utilization":
		return "Memory", "Memory utilization", "", "", true
	case "vm.memory.size", "vm.memory.dependent.size":
		switch param(p, 0) {
		case "pavailable":
			return "Memory", "Available memory %", "", "", true
		case "available":
			return "Memory", "Available memory", "", "", true
		case "pused":
			return "Memory", "Used memory %", "", "", true
		case "used":
			return "Memory", "Used memory", "", "", true
		case "total":
			return "Memory", "Total memory", "", "", true
		}

	case "vfs.fs.size", "vfs.fs.dependent.size":
		mount := param(p, 0)
		switch param(p, 1) {
		case "pused":
			return "Disk", "Disk used % (" + mount + ")", mount, "Used %", true
		case "used":
			return "Disk", "Disk used (" + mount + ")", mount, "Used", true
		case "total":
			return "Disk", "Disk total (" + mount + ")", mount, "Total", true
		case "pfree":
			return "Disk", "Disk free % (" + mount + ")", mount, "Free %", true
		case "free":
			return "Disk", "Disk free (" + mount + ")", mount, "Free", true
		}

	case "net.if.in", "net.if.dependent.in":
		iface := ifaceName(name, p)
		return "Network", trafficLabel("Traffic in", iface, p), iface, chanMode("In", p), true
	case "net.if.out", "net.if.dependent.out":
		iface := ifaceName(name, p)
		return "Network", trafficLabel("Traffic out", iface, p), iface, chanMode("Out", p), true

	case "system.uptime":
		return "Uptime", "Uptime", "", "", true

	// Windows services (Argus Windows by SNMP, LAN Manager svSvcTable). The service name rides in
	// the item name (the LLD prototype is named "{#WINSVC}"); each is its own flat row.
	case "win.service.state":
		return "Services", name, "", "", true

	// UniFi devices, polled from the controller (Argus UniFi Switch by HTTP). The raw master item
	// (unifi.switch.raw) deliberately doesn't match - it's plumbing, visible under "All sensors".
	// unifi.device.state is also left uncurated (user call: not a useful reading) - it still feeds
	// the "switch offline on the controller" trigger, it just doesn't take up a sensor row.
	case "unifi.firmware":
		return "Status", "Firmware version", "", "", true
	case "unifi.cpu.util":
		return "CPU", "CPU utilization", "", "", true
	case "unifi.mem.util":
		return "Memory", "Memory utilization", "", "", true
	case "unifi.uptime":
		return "Uptime", "Uptime", "", "", true
	case "unifi.uplink.in":
		return "Network", "Uplink traffic in", "Uplink", "In", true
	case "unifi.uplink.out":
		return "Network", "Uplink traffic out", "Uplink", "Out", true
	case "unifi.port.state", "unifi.port.speed", "unifi.port.in", "unifi.port.out", "unifi.port.poe":
		// The operator's port name from the controller rides in the item name ("Port 3 link
		// (Shield-TV)"); lift it into the group label so the tree reads "Port 3 · Shield-TV".
		inst := "Port " + param(p, 0)
		if pn := parenSuffix(name); pn != "" && pn != inst {
			inst += " · " + pn
		}
		ch := map[string]string{"unifi.port.state": "Link", "unifi.port.speed": "Speed", "unifi.port.in": "In", "unifi.port.out": "Out", "unifi.port.poe": "PoE"}[base]
		return "Ports", name, inst, ch, true
	case "unifi.poe.total":
		return "Power", "PoE power draw", "", "", true

	// UniFi access points (Argus UniFi AP by HTTP): the wireless side gets its own category -
	// total clients + experience flat, and a per-radio group (clients + channel utilization) per
	// band. The band label rides in the key's second parameter ("2.4 GHz", "5 GHz", …).
	case "unifi.clients":
		return "Wireless", "Connected clients", "", "", true
	case "unifi.experience":
		return "Wireless", "Experience score", "", "", true
	case "unifi.radio.clients", "unifi.radio.util":
		inst := "Radio"
		if band := param(p, 1); band != "" {
			inst = "Radio " + band
		}
		ch := "Clients"
		if base == "unifi.radio.util" {
			ch = "Utilization"
		}
		return "Wireless", name, inst, ch, true

	// UniFi gateways (Argus UniFi Gateway by HTTP): per-WAN traffic groups like a NIC, and the
	// gateway's own uplink-monitor pings land under Ping as a "WAN quality" group beside ICMP.
	case "unifi.wan.in":
		return "Network", name, wanInstance(param(p, 0)), "In", true
	case "unifi.wan.out":
		return "Network", name, wanInstance(param(p, 0)), "Out", true
	case "unifi.wan.latency":
		return "Ping", name, wanInstance(param(p, 0)) + " quality", "Response time", true
	case "unifi.wan.avail":
		return "Ping", name, wanInstance(param(p, 0)) + " quality", "Availability", true
	case "unifi.speedtest.down":
		return "Network", "Speedtest download", "", "", true
	case "unifi.speedtest.up":
		return "Network", "Speedtest upload", "", "", true

	// UniFi OS Console (Argus UniFi OS Console by HTTP): the raw master is plumbing; internal
	// storage volumes land under Disk (one flat "used %" row per volume, name in the label).
	case "unifi.console.raw":
		return "", "", "", "", false
	case "unifi.storage.pused":
		return "Disk", "Storage used % (" + param(p, 0) + ")", "", "", true

	// unRAID (Argus unRAID by SNMP, attached alongside the Linux template): the SNMP plugin's
	// extend scripts deliver per-disk temperatures - grouped into ONE overlay chart - and
	// per-share free space (flat rows under Disk, after the mounts).
	case "unraid.disktemp.raw", "unraid.sharefree.raw", "unraid.pooltemp.raw", "unraid.arraytemp.raw":
		return "", "", "", "", false // plumbing masters ("...temperatures..." must not hit the name heuristic)
	case "unraid.disktemp", "unraid.arraytemp", "unraid.pooltemp":
		// Array drives (plugin disktemp extend, or the atomic arraytemps extend where installed)
		// and pool/cache drives (pooltemps extend) merge into the same group - one overlay chart
		// for every drive in the box. hosts.go hides a disktemp row its arraytemp twin supersedes.
		// The channel is the unRAID slot ("parity", "disk1", "cache") when the item name carries
		// one - script v3 emits it - else the drive id from the key (v2 output, plugin chain).
		ch := parenSuffix(name)
		if ch == "" {
			ch = param(p, 0)
		}
		return "Temperature", name, "Disk temperatures", ch, true
	case "unraid.sharefree":
		return "Disk", name, "", "", true

	// HTTP/HTTPS endpoint add-on (Argus HTTP Endpoint template). The key params are macros
	// ({$HTTP.SCHEME}/{$HTTP.PORT}), so the label is fixed rather than derived from them. The two
	// items group like ICMP: response time is the primary channel, reachability the Downtime band
	// (same "Reachable"/"Response time" channel names the Ping/Web frontend branches key on).
	case "net.tcp.service":
		return "Web", "HTTP/HTTPS reachable", "HTTP/HTTPS", "Reachable", true
	case "net.tcp.service.perf":
		return "Web", "HTTP/HTTPS response time", "HTTP/HTTPS", "Response time", true
	}

	// Heuristic fallback for temperature sensors, whose keys vary widely by template/SNMP.
	low := strings.ToLower(name)
	if strings.Contains(low, "temperature") || strings.Contains(low, " temp") {
		return "Temperature", name, "", "", true
	}
	return "", "", "", "", false
}
