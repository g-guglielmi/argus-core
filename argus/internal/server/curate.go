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
	"Temperature": 7,
	"Uptime":      8,
	"Ports":       9,
	"Status":      10,
}
var categoryOrderNet = map[string]int{
	"Ping":        0,
	"Web":         1,
	"Network":     2,
	"Power":       3, // before CPU (user call)
	"CPU":         4,
	"Memory":      5,
	"Disk":        6,
	"Uptime":      7,
	"Ports":       8,
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
