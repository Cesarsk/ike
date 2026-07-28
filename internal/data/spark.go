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

// Top level is ▇ (7/8 block), not █: some terminal fonts render U+2588 FULL
// BLOCK with a broken/slashed glyph, which made peak sparkline points look
// like "ØØ" in the wild.
var sparkLevels = []rune("▁▂▃▄▅▆▇")

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

// ChartRows renders a series as a multi-row block chart (top row first) —
// the taller sibling of Sparkline for panes with vertical room. Points are
// bucket-averaged down to width columns; column heights are drawn in
// eighth-block resolution. A flat series draws its single level mid-chart.
func ChartRows(points []float64, width, height int) []string {
	if len(points) == 0 || width <= 0 || height <= 0 {
		return nil
	}
	// Downsample to one column per bucket (average).
	cols := make([]float64, 0, width)
	if len(points) <= width {
		cols = append(cols, points...)
	} else {
		per := float64(len(points)) / float64(width)
		for c := 0; c < width; c++ {
			lo, hi := int(float64(c)*per), int(float64(c+1)*per)
			if hi > len(points) {
				hi = len(points)
			}
			if lo >= hi {
				lo = hi - 1
			}
			sum := 0.0
			for _, p := range points[lo:hi] {
				sum += p
			}
			cols = append(cols, sum/float64(hi-lo))
		}
	}
	min, max := cols[0], cols[0]
	for _, v := range cols {
		min = math.Min(min, v)
		max = math.Max(max, v)
	}
	span := max - min
	rows := make([][]rune, height)
	for r := range rows {
		rows[r] = make([]rune, len(cols))
		for c := range rows[r] {
			rows[r][c] = ' '
		}
	}
	for c, v := range cols {
		var h8 int
		if span == 0 {
			h8 = height * 4 // flat: a mid-height line
		} else {
			h8 = int((v - min) / span * float64(height*8))
			if h8 < 1 {
				h8 = 1 // a visible baseline beats an empty column
			}
		}
		for r := 0; r < height; r++ { // r = rows from the bottom
			rowIdx := height - 1 - r
			switch {
			case h8 >= (r+1)*8:
				rows[rowIdx][c] = sparkLevels[len(sparkLevels)-1]
			case h8 > r*8:
				rows[rowIdx][c] = sparkLevels[(h8-r*8-1)*len(sparkLevels)/8]
			}
		}
	}
	out := make([]string, height)
	for r := range rows {
		out[r] = string(rows[r])
	}
	return out
}
