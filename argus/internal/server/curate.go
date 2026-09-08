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

// categoryOrder controls how categories are grouped/sorted in the curated view (user-specified:
// availability first, then traffic, compute, storage, uptime, ports; Status metadata last).
var categoryOrder = map[string]int{
	"Ping":        0,
	"Web":         1,
	"Network":     2,
	"CPU":         3,
	"Memory":      4,
	"Disk":        5,
	"Uptime":      6,
	"Ports":       7,
	"Power":       8,
	"Temperature": 9,
	"Status":      10,
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

// trafficLabel builds a network-traffic label, distinguishing the byte-rate item from the
// per-interface error/dropped/packet counters that share the net.if.in/out key. Params are
// [interface, mode]; mode "" or "bytes" is the main rate.
func trafficLabel(base string, p []string) string {
	iface, mode := param(p, 0), param(p, 1)
	label := base
	if mode != "" && mode != "bytes" {
		label += " " + mode
	}
	if iface != "" {
		label += " (" + iface + ")"
	}
	return label
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
		return "Network", trafficLabel("Traffic in", p), param(p, 0), chanMode("In", p), true
	case "net.if.out", "net.if.dependent.out":
		return "Network", trafficLabel("Traffic out", p), param(p, 0), chanMode("Out", p), true

	case "system.uptime":
		return "Uptime", "Uptime", "", "", true

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
	case "unifi.port.state":
		return "Ports", "Port " + param(p, 0) + " link", "Port " + param(p, 0), "Link", true
	case "unifi.port.speed":
		return "Ports", "Port " + param(p, 0) + " speed", "Port " + param(p, 0), "Speed", true
	case "unifi.port.in":
		return "Ports", "Port " + param(p, 0) + " traffic in", "Port " + param(p, 0), "In", true
	case "unifi.port.out":
		return "Ports", "Port " + param(p, 0) + " traffic out", "Port " + param(p, 0), "Out", true
	case "unifi.port.poe":
		return "Ports", "Port " + param(p, 0) + " PoE power", "Port " + param(p, 0), "PoE", true
	case "unifi.poe.total":
		return "Power", "PoE power draw", "", "", true

	// HTTP/HTTPS endpoint add-on (Argus HTTP Endpoint template). The key params are macros
	// ({$HTTP.SCHEME}/{$HTTP.PORT}), so the label is fixed rather than derived from them.
	case "net.tcp.service":
		return "Web", "HTTP/HTTPS reachable", "", "", true
	case "net.tcp.service.perf":
		return "Web", "HTTP/HTTPS response time", "", "", true
	}

	// Heuristic fallback for temperature sensors, whose keys vary widely by template/SNMP.
	low := strings.ToLower(name)
	if strings.Contains(low, "temperature") || strings.Contains(low, " temp") {
		return "Temperature", name, "", "", true
	}
	return "", "", "", "", false
}
