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
