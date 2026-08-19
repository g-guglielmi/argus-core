package server

import "testing"

func TestAppUpdateStatus(t *testing.T) {
	cases := []struct {
		name       string
		cur, latest string
		wantStatus string
		wantUpdate bool
	}{
		{"unstamped local build", "", "v0.4.10", "dev", false},
		{"bare sha build", "abc1234", "v0.4.10", "dev", false},
		{"latest not resolved yet", "v0.4.10", "", "unknown", false},
		{"exact newest release", "v0.4.10", "v0.4.10", "current", false},
		{"pinned older release", "v0.4.9", "v0.4.10", "outdated", true},
		{"older by minor", "v0.3.0", "v0.4.10", "outdated", true},
		{"latest build ahead of last release", "v0.4.10-3-gabc1234", "v0.4.10", "current", false},
		{"dev build one release behind", "v0.4.9-2-gdead", "v0.4.10", "outdated", true},
		{"ahead of published (newer major)", "v1.0.0", "v0.4.10", "current", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotStatus, gotUpdate := appUpdateStatus(c.cur, c.latest)
			if gotStatus != c.wantStatus || gotUpdate != c.wantUpdate {
				t.Errorf("appUpdateStatus(%q, %q) = (%q, %v), want (%q, %v)",
					c.cur, c.latest, gotStatus, gotUpdate, c.wantStatus, c.wantUpdate)
			}
		})
	}
}
