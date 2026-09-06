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

// categoryOrder controls how categories are grouped/sorted in the curated view.
var categoryOrder = map[string]int{
	"Ping":        0,
	"Web":         1,
	"CPU":         2,
	"Memory":      3,
	"Disk":        4,
	"Network":     5,
	"Temperature": 6,
	"Uptime":      7,
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

// classifyItem returns (category, label, matched) for a Zabbix item key/name.
func classifyItem(key, name string) (string, string, bool) {
	base, p := splitKey(key)

	switch base {
	case "icmpping":
		return "Ping", "Reachable (ICMP)", true
	case "icmppingloss":
		return "Ping", "ICMP loss", true
	case "icmppingsec":
		return "Ping", "ICMP response time", true

	case "system.cpu.util":
		// The Linux template has one item per CPU state, most of them near-zero noise. Keep
		// only the meaningful states in the curated view; the rest fall under "All sensors".
		state := param(p, 1)
		if !cpuUtilKeep[state] {
			return "", "", false
		}
		if state != "" {
			return "CPU", "CPU utilization (" + state + ")", true
		}
		return "CPU", "CPU utilization", true
	case "system.cpu.load":
		if a := param(p, 1); a != "" {
			return "CPU", "CPU load (" + a + ")", true
		}
		return "CPU", "CPU load", true

	case "vm.memory.utilization":
		return "Memory", "Memory utilization", true
	case "vm.memory.size", "vm.memory.dependent.size":
		switch param(p, 0) {
		case "pavailable":
			return "Memory", "Available memory %", true
		case "available":
			return "Memory", "Available memory", true
		case "pused":
			return "Memory", "Used memory %", true
		case "used":
			return "Memory", "Used memory", true
		case "total":
			return "Memory", "Total memory", true
		}

	case "vfs.fs.size", "vfs.fs.dependent.size":
		mount := param(p, 0)
		switch param(p, 1) {
		case "pused":
			return "Disk", "Disk used % (" + mount + ")", true
		case "used":
			return "Disk", "Disk used (" + mount + ")", true
		case "total":
			return "Disk", "Disk total (" + mount + ")", true
		case "pfree":
			return "Disk", "Disk free % (" + mount + ")", true
		case "free":
			return "Disk", "Disk free (" + mount + ")", true
		}

	case "net.if.in", "net.if.dependent.in":
		return "Network", trafficLabel("Traffic in", p), true
	case "net.if.out", "net.if.dependent.out":
		return "Network", trafficLabel("Traffic out", p), true

	case "system.uptime":
		return "Uptime", "Uptime", true

	// HTTP/HTTPS endpoint add-on (Argus HTTP Endpoint template). The key params are macros
	// ({$HTTP.SCHEME}/{$HTTP.PORT}), so the label is fixed rather than derived from them.
	case "net.tcp.service":
		return "Web", "HTTP/HTTPS reachable", true
	case "net.tcp.service.perf":
		return "Web", "HTTP/HTTPS response time", true
	}

	// Heuristic fallback for temperature sensors, whose keys vary widely by template/SNMP.
	low := strings.ToLower(name)
	if strings.Contains(low, "temperature") || strings.Contains(low, " temp") {
		return "Temperature", name, true
	}
	return "", "", false
}
