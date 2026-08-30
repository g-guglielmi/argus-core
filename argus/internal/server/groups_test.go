package server

import (
	"strings"
	"testing"
)

func TestCleanGroupName(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"Network", "Network", true},
		{"  padded  ", "padded", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("a", maxGroupNameLen+1), "", false}, // over the length cap
	}
	for _, c := range cases {
		got, ok := cleanGroupName(c.in)
		if ok != c.valid {
			t.Errorf("cleanGroupName(%q) valid=%v, want %v", c.in, ok, c.valid)
		}
		if ok && got != c.want {
			t.Errorf("cleanGroupName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
