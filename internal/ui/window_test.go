package ui

import (
	"testing"
	"time"
)

func TestParseWindow(t *testing.T) {
	cases := map[string]time.Duration{
		"45m": 45 * time.Minute,
		"4h":  4 * time.Hour,
		"2d":  48 * time.Hour,
		"1w":  7 * 24 * time.Hour,
		"1mo": 30 * 24 * time.Hour,
		"90s": 90 * time.Second,
	}
	for in, want := range cases {
		got, err := parseWindow(in)
		if err != nil || got != want {
			t.Errorf("parseWindow(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "-2d", "mo"} {
		if _, err := parseWindow(bad); err == nil {
			t.Errorf("parseWindow(%q) should fail", bad)
		}
	}
}
