package server

import "testing"

func TestAxisNum(t *testing.T) {
	cases := map[float64]string{
		0: "0", 11: "11", 3.14159: "3.14", 706000: "706k",
		70600000: "70.6M", 35300000: "35.3M", 1200000000: "1.2G", 0.5: "0.5", -4200: "-4.2k",
	}
	for in, want := range cases {
		if got := axisNum(in); got != want {
			t.Errorf("axisNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAxisLabel(t *testing.T) {
	cases := []struct {
		v     float64
		units string
		want  string
	}{
		{70600000, "uptime", "817.1d"}, // uptime seconds → days
		{708480, "uptime", "8.2d"},
		{4680, "uptime", "1.3h"},
		{45, "uptime", "45s"},
		{5368709120, "B", "5GB"},       // bytes, 1024-based
		{1610612736, "B", "1.5GB"},     // 1.5 GiB
		{512, "B", "512B"},             // base unit → integer, no scaling
		{45000000, "bps", "45Mbps"},    // bits, 1000-based
		{5000000, "Bps", "4.77MBps"},   // bytes/s → MBps
		{45.2, "%", "45.2%"},           // arbitrary unit appended
		{70600000, "", "70.6M"},        // unitless → SI compact
	}
	for _, c := range cases {
		if got := axisLabel(c.v, c.units); got != c.want {
			t.Errorf("axisLabel(%v, %q) = %q, want %q", c.v, c.units, got, c.want)
		}
	}
}
