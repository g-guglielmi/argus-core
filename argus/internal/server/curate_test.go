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
