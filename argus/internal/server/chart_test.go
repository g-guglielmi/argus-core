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
