package server

import "testing"

func TestUpdaterStatus(t *testing.T) {
	cases := []struct{ reported, latest, want string }{
		{"", "0.2.3", "unknown"},        // sidecar version not reported yet
		{"0.2.3", "", "unknown"},        // GHCR not resolved yet
		{"0.2.3", "0.2.3", "current"},   // equal
		{"v0.2.3", "0.2.3", "current"},  // leading "v" ignored on the reported side
		{"0.2.3", "v0.2.4", "outdated"}, // and on the latest side
		{"0.2.2", "0.2.3", "outdated"},  // older
	}
	for _, c := range cases {
		if got := updaterStatus(c.reported, c.latest); got != c.want {
			t.Errorf("updaterStatus(%q,%q) = %q, want %q", c.reported, c.latest, got, c.want)
		}
	}
}
