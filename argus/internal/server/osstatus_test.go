package server

import "testing"

func TestRebootWindowValid(t *testing.T) {
	cases := []struct {
		name string
		rw   rebootWindow
		want bool
	}{
		{"notify ignores time fields", rebootWindow{Mode: "notify"}, true},
		{"notify with junk time still valid", rebootWindow{Mode: "notify", Weekday: 99, Hour: 99}, true},
		{"auto in range", rebootWindow{Mode: "auto", Weekday: 3, Hour: 3, Minute: 30}, true},
		{"auto sunday midnight", rebootWindow{Mode: "auto", Weekday: 0, Hour: 0, Minute: 0}, true},
		{"auto saturday last minute", rebootWindow{Mode: "auto", Weekday: 6, Hour: 23, Minute: 59}, true},
		{"empty mode", rebootWindow{}, false},
		{"unknown mode", rebootWindow{Mode: "sometimes"}, false},
		{"auto weekday too high", rebootWindow{Mode: "auto", Weekday: 7}, false},
		{"auto hour too high", rebootWindow{Mode: "auto", Hour: 24}, false},
		{"auto minute too high", rebootWindow{Mode: "auto", Minute: 60}, false},
		{"auto negative", rebootWindow{Mode: "auto", Weekday: -1}, false},
	}
	for _, c := range cases {
		if got := c.rw.valid(); got != c.want {
			t.Errorf("%s: valid() = %v, want %v", c.name, got, c.want)
		}
	}
	if defaultRebootWindow().Mode != "notify" {
		t.Errorf("default reboot window mode = %q, want notify (core must never reboot unannounced)", defaultRebootWindow().Mode)
	}
}
