package server

import "testing"

// The UniFi port sensors carry the controller's port name as a "(...)" suffix in the item name;
// classifyItem lifts it into the group instance so the tree reads "Port 3 · Shield-TV".
func TestClassifyUnifiPorts(t *testing.T) {
	cases := []struct {
		key, name string
		inst, ch  string
	}{
		{"unifi.port.in[3]", "Port 3 traffic in (Shield-TV)", "Port 3 · Shield-TV", "In"},
		{"unifi.port.state[3]", "Port 3 link (Shield-TV)", "Port 3 · Shield-TV", "Link"},
		{"unifi.port.poe[4]", "Port 4 PoE power (Cam-Garage)", "Port 4 · Cam-Garage", "PoE"},
		// an unnamed port: the controller default name equals "Port N" - no redundant suffix
		{"unifi.port.speed[5]", "Port 5 speed (Port 5)", "Port 5", "Speed"},
		{"unifi.port.out[6]", "Port 6 traffic out (Port 6)", "Port 6", "Out"},
		// pre-rename items (no suffix at all) keep grouping by index
		{"unifi.port.state[7]", "Port 7 link", "Port 7", "Link"},
	}
	for _, c := range cases {
		cat, _, inst, ch, ok := classifyItem(c.key, c.name)
		if !ok || cat != "Ports" {
			t.Fatalf("%s: classified (%q, ok=%v), want Ports", c.key, cat, ok)
		}
		if inst != c.inst || ch != c.ch {
			t.Errorf("%s: got instance %q channel %q, want %q %q", c.key, inst, ch, c.inst, c.ch)
		}
	}
}

// AP radios group per band under Wireless; gateway WANs split traffic (Network) from the uplink
// monitors (Ping, "... quality"), and the one-WAN case drops the index from the group name.
func TestClassifyUnifiWirelessAndWan(t *testing.T) {
	cases := []struct {
		key, name          string
		cat, inst, channel string
	}{
		{"unifi.clients", "Connected clients", "Wireless", "", ""},
		{"unifi.experience", "Experience score", "Wireless", "", ""},
		{`unifi.radio.clients[wifi0,"2.4 GHz"]`, "Radio 2.4 GHz clients", "Wireless", "Radio 2.4 GHz", "Clients"},
		{`unifi.radio.util[wifi1,"5 GHz"]`, "Radio 5 GHz channel utilization", "Wireless", "Radio 5 GHz", "Utilization"},
		{"unifi.wan.in[1]", "WAN 1 traffic in (wan1)", "Network", "WAN", "In"},
		{"unifi.wan.out[2]", "WAN 2 traffic out (wan2)", "Network", "WAN 2", "Out"},
		{"unifi.wan.latency[1]", "WAN 1 monitor latency (wan1)", "Ping", "WAN quality", "Response time"},
		{"unifi.wan.avail[2]", "WAN 2 availability (wan2)", "Ping", "WAN 2 quality", "Availability"},
		{"unifi.speedtest.down", "Speedtest download", "Network", "", ""},
	}
	for _, c := range cases {
		cat, _, inst, ch, ok := classifyItem(c.key, c.name)
		if !ok {
			t.Fatalf("%s: not classified", c.key)
		}
		if cat != c.cat || inst != c.inst || ch != c.channel {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)", c.key, cat, inst, ch, c.cat, c.inst, c.channel)
		}
	}
}

// The HTTP add-on's two items group like ICMP: reachability + response time under one instance,
// with the "Reachable"/"Response time" channels the frontend turns into Downtime + primary.
func TestClassifyWebGroup(t *testing.T) {
	cat, _, inst, ch, ok := classifyItem("net.tcp.service.perf[{$HTTP.SCHEME},,{$HTTP.PORT}]", "HTTP/HTTPS response time")
	if !ok || cat != "Web" || inst != "HTTP/HTTPS" || ch != "Response time" {
		t.Errorf("perf: got (%q, %q, %q, ok=%v)", cat, inst, ch, ok)
	}
	cat, _, inst, ch, ok = classifyItem("net.tcp.service[{$HTTP.SCHEME},,{$HTTP.PORT}]", "HTTP/HTTPS reachable")
	if !ok || cat != "Web" || inst != "HTTP/HTTPS" || ch != "Reachable" {
		t.Errorf("reachable: got (%q, %q, %q, ok=%v)", cat, inst, ch, ok)
	}
}

// A UniFi OS Console's internal storage lands under Disk; the raw master stays uncurated.
func TestClassifyUnifiConsole(t *testing.T) {
	cat, label, _, _, ok := classifyItem("unifi.storage.pused[eMMC]", "Storage used % (eMMC)")
	if !ok || cat != "Disk" || label != "Storage used % (eMMC)" {
		t.Errorf("console storage: got (%q, %q, ok=%v)", cat, label, ok)
	}
	if _, _, _, _, ok := classifyItem("unifi.console.raw", "UniFi raw device data"); ok {
		t.Error("console.raw master must stay uncurated")
	}
}

// A network interface is labelled by the friendly name lifted from the item name (Windows uses
// ifAlias) when present, and grouped by it so In/Out stay together; it falls back to the key param
// (ifName) when the item name carries none - which is the Linux case, so nothing changes there.
func TestClassifyNetIfName(t *testing.T) {
	// Windows: friendly ifAlias in the item name, ugly ifName in the key.
	cat, label, inst, ch, ok := classifyItem("net.if.in[ethernet_32770]", "Traffic in (Ethernet 2)")
	if !ok || cat != "Network" || inst != "Ethernet 2" || label != "Traffic in (Ethernet 2)" || ch != "In" {
		t.Errorf("windows nic: got (%q, %q, %q, %q, ok=%v)", cat, label, inst, ch, ok)
	}
	// In and Out land in the same instance so they group.
	_, _, instOut, chOut, _ := classifyItem("net.if.out[ethernet_32770]", "Traffic out (Ethernet 2)")
	if instOut != "Ethernet 2" || chOut != "Out" {
		t.Errorf("windows nic out: got instance %q channel %q", instOut, chOut)
	}
	// Linux: item name equals the key param, so behaviour is unchanged.
	_, llabel, linst, _, _ := classifyItem("net.if.in[enp1s0]", "Traffic in (enp1s0)")
	if linst != "enp1s0" || llabel != "Traffic in (enp1s0)" {
		t.Errorf("linux nic: got instance %q label %q", linst, llabel)
	}
	// Empty alias in the name falls back to the key param.
	_, _, finst, _, _ := classifyItem("net.if.in[ethernet_32770]", "Traffic in ()")
	if finst != "ethernet_32770" {
		t.Errorf("empty-alias fallback: got instance %q", finst)
	}
}

// Windows services (LAN Manager svSvcTable) land under a Services category, labelled by name.
func TestClassifyWindowsService(t *testing.T) {
	cat, label, _, _, ok := classifyItem("win.service.state[DNS Server]", "DNS Server")
	if !ok || cat != "Services" || label != "DNS Server" {
		t.Errorf("win service: got (%q, %q, ok=%v)", cat, label, ok)
	}
	// Windows reuses the Linux memory/CPU keys, so they classify identically.
	if cat, _, _, _, ok := classifyItem("vm.memory.utilization[snmp]", "Memory utilization"); !ok || cat != "Memory" {
		t.Errorf("windows memory key: got (%q, ok=%v)", cat, ok)
	}
}

// Per-core CPU loads group into one "Cores" instance with sequential core-number channels.
func TestClassifyCpuCores(t *testing.T) {
	cat, label, inst, ch, ok := classifyItem("system.cpu.core[7]", "Core 7 load")
	if !ok || cat != "CPU" || label != "Core 7 load" || inst != "Cores" || ch != "Core 7" {
		t.Errorf("cpu core: got (%q, %q, %q, %q, ok=%v)", cat, label, inst, ch, ok)
	}
}

// unRAID disk temps group into ONE "Disk temperatures" instance (channel = the drive id); shares
// are flat Disk rows; the raw extend masters stay uncurated even though their names say
// "temperatures" (the heuristic must not catch them).
func TestClassifyUnraid(t *testing.T) {
	cat, _, inst, ch, ok := classifyItem(`unraid.disktemp["WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F"]`, "Disk temperature (WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F)")
	if !ok || cat != "Temperature" || inst != "Disk temperatures" || ch != "WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F" {
		t.Errorf("disktemp: got (%q, %q, %q, ok=%v)", cat, inst, ch, ok)
	}
	// The atomic arraytemps extend lands in the same group; the channel is the unRAID slot when
	// the name carries one (script v3), so the legend reads "parity"/"disk1" instead of serials.
	acat, _, ainst, ach, aok := classifyItem(`unraid.arraytemp["WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F"]`, "Disk temperature (parity)")
	if !aok || acat != "Temperature" || ainst != "Disk temperatures" || ach != "parity" {
		t.Errorf("arraytemp slot: got (%q, %q, %q, ok=%v)", acat, ainst, ach, aok)
	}
	// v2 output / not-yet-rediscovered items keep the drive id as the channel.
	_, _, _, ach2, _ := classifyItem(`unraid.arraytemp["WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F"]`, "Disk temperature (WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F)")
	if ach2 != "WDC WD30EFRX-68EUZN0 WD-WMC4N0E5EJ0F" {
		t.Errorf("arraytemp id fallback: got channel %q", ach2)
	}
	// Pool drives (optional pooltemps extend) land in the SAME group as array drives.
	pcat, _, pinst, pch, pok := classifyItem(`unraid.pooltemp["Samsung_SSD_970_EVO_1TB_S000000000"]`, "Disk temperature (Samsung_SSD_970_EVO_1TB_S000000000)")
	if !pok || pcat != "Temperature" || pinst != "Disk temperatures" || pch != "Samsung_SSD_970_EVO_1TB_S000000000" {
		t.Errorf("pooltemp: got (%q, %q, %q, ok=%v)", pcat, pinst, pch, pok)
	}
	cat, label, inst, _, ok := classifyItem(`unraid.sharefree["appdata"]`, "Share free (appdata)")
	if !ok || cat != "Disk" || inst != "" || label != "Share free (appdata)" {
		t.Errorf("sharefree: got (%q, %q, %q, ok=%v)", cat, label, inst, ok)
	}
	if _, _, _, _, ok := classifyItem("unraid.disktemp.raw", "unRAID disk temperatures (raw)"); ok {
		t.Error("disktemp.raw master must stay uncurated")
	}
	if _, _, _, _, ok := classifyItem("unraid.sharefree.raw", "unRAID share free space (raw)"); ok {
		t.Error("sharefree.raw master must stay uncurated")
	}
	if _, _, _, _, ok := classifyItem("unraid.pooltemp.raw", "unRAID pool disk temperatures (raw)"); ok {
		t.Error("pooltemp.raw master must stay uncurated")
	}
	if _, _, _, _, ok := classifyItem("unraid.arraytemp.raw", "unRAID array disk temperatures (raw)"); ok {
		t.Error("arraytemp.raw master must stay uncurated")
	}
}

// AdGuard's DNS stats land under a DNS category (with the proxy-run net.dns resolve check), its
// protection/version under Status; Home Assistant's platform health lands under Status. Both raw
// masters stay uncurated.
func TestClassifyHTTPServices(t *testing.T) {
	cases := []struct {
		key, name string
		cat, ch   string
	}{
		{"adguard.block_pct", "Block rate", "DNS", ""},
		{"adguard.queries", "DNS queries", "DNS", ""},
		{"adguard.blocked", "Blocked queries", "DNS", ""},
		{"adguard.avg_time", "Average processing time", "DNS", ""},
		{"net.dns[{HOST.CONN},example.com]", "DNS resolves (example.com)", "DNS", ""},
		{"adguard.protection", "Protection enabled", "Status", ""},
		{"adguard.version", "Version", "Status", ""},
		{"hass.version", "Version", "Status", ""},
		{"hass.integrations", "Integrations", "Status", ""},
		{"hass.entities", "Entities", "Status", ""},
		{"hass.unavailable", "Unavailable entities", "Status", ""},
	}
	for _, c := range cases {
		cat, _, _, ch, ok := classifyItem(c.key, c.name)
		if !ok || cat != c.cat || ch != c.ch {
			t.Errorf("%s: got (%q, ch %q, ok=%v), want (%q, ch %q)", c.key, cat, ch, ok, c.cat, c.ch)
		}
	}
	// The raw masters and the *.running health bits are plumbing (they drive down triggers), not
	// sensor rows.
	for _, k := range []string{"adguard.raw", "adguard.running", "hass.raw", "hass.running"} {
		if _, _, _, _, ok := classifyItem(k, k); ok {
			t.Errorf("%s must stay uncurated", k)
		}
	}
}

// Capability placeholders match on the key base, so per-instance keys are covered.
func TestHideZero(t *testing.T) {
	for _, k := range []string{"unifi.temp", "unifi.poe.total", "unifi.wan.latency[1]", "unifi.speedtest.down"} {
		if !hideZero(k) {
			t.Errorf("hideZero(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"unifi.wan.avail[1]", "unifi.wan.in[1]", "unifi.clients", "icmpping"} {
		if hideZero(k) {
			t.Errorf("hideZero(%q) = true, want false", k)
		}
	}
}

// Every order profile must rank every category classifyItem can produce - a missing key silently
// sorts as 0 (Ping-level), which is exactly the kind of bug this guards against.
func TestCategoryOrdersComplete(t *testing.T) {
	for name, m := range map[string]map[string]int{"server": categoryOrderServer, "net": categoryOrderNet, "nas": categoryOrderNAS} {
		if len(m) != len(categoryOrderServer) {
			t.Errorf("%s order has %d categories, server has %d", name, len(m), len(categoryOrderServer))
		}
		for cat := range categoryOrderServer {
			if _, ok := m[cat]; !ok {
				t.Errorf("%s order missing category %q", name, cat)
			}
		}
	}
	if categoryOrderNAS["Temperature"] >= categoryOrderNAS["Disk"] {
		t.Error("NAS order must put Temperature before Disk")
	}
}

// Labels with embedded numbers sort numerically ("Port 2" before "Port 10").
func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Port 2 link", "Port 10 link", true},
		{"Port 10 link", "Port 2 link", false},
		{"Port 2 link", "Port 2 speed", true},
		{"Traffic in (eth2)", "Traffic in (eth10)", true},
		{"Port 2", "Port 2", false},
		{"Port 02", "Port 2", false}, // equal numerically; equal-length tiebreak keeps order stable
		{"CPU", "Memory", true},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
