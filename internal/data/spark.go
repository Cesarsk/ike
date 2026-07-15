package data

import (
	"fmt"
	"math"
	"strings"
)

// rangeSeconds parses a Datadog relative "from" like "now-1h"/"now-15m"/
// "now-7d" into seconds. Returns false if it can't.
func rangeSeconds(r string) (int, bool) {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "now-")
	if r == "" {
		return 0, false
	}
	unit := r[len(r)-1]
	numStr := r[:len(r)-1]
	n := 0
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 0, false
	}
	switch unit {
	case 's':
		return n, true
	case 'm':
		return n * 60, true
	case 'h':
		return n * 3600, true
	case 'd':
		return n * 86400, true
	case 'w':
		return n * 604800, true
	}
	return 0, false
}

var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders a series as block characters. A flat or empty series is
// handled gracefully (mid-level / empty string). This is the terminal-native
// substitute for a Datadog timeseries graph — trend at a glance, not fidelity.
func Sparkline(points []float64) string {
	if len(points) == 0 {
		return ""
	}
	min, max := points[0], points[0]
	for _, p := range points {
		min = math.Min(min, p)
		max = math.Max(max, p)
	}
	var b strings.Builder
	span := max - min
	for _, p := range points {
		if span == 0 {
			b.WriteRune(sparkLevels[len(sparkLevels)/2])
			continue
		}
		idx := int((p - min) / span * float64(len(sparkLevels)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkLevels) {
			idx = len(sparkLevels) - 1
		}
		b.WriteRune(sparkLevels[idx])
	}
	return b.String()
}

// FormatValue renders a metric value compactly (1.2k, 3.4M, 45, 0.87).
func FormatValue(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1e9:
		return fmt.Sprintf("%.1fG", v/1e9)
	case abs >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case abs >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	case abs >= 10:
		return fmt.Sprintf("%.0f", v)
	case abs == 0:
		return "0"
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
