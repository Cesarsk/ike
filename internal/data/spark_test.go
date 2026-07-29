package data

import (
	"math"
	"strings"
	"testing"
)

func TestChartRowsFlatIsThinLine(t *testing.T) {
	rows := ChartRows([]float64{5, 5, 5, 5}, 4, 4)
	if len(rows) != 4 {
		t.Fatalf("rows: %d", len(rows))
	}
	filled := 0
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			filled++
		}
	}
	if filled != 1 {
		t.Errorf("flat series should draw exactly one thin line row, got %d filled rows:\n%q", filled, rows)
	}
}

func TestChartRowsNaNGapKeepsColumnBlank(t *testing.T) {
	pts := []float64{1, math.NaN(), 3}
	rows := ChartRows(pts, 3, 2)
	for _, r := range rows {
		if []rune(r)[1] != ' ' {
			t.Fatalf("NaN column should be blank in every row:\n%q", rows)
		}
	}
	// The valid columns still chart.
	bottom := []rune(rows[1])
	if bottom[0] == ' ' || bottom[2] == ' ' {
		t.Errorf("valid columns should draw:\n%q", rows)
	}
}

func TestSparklineNaNGap(t *testing.T) {
	got := Sparkline([]float64{1, math.NaN(), 3})
	if []rune(got)[1] != ' ' {
		t.Errorf("NaN should render as a gap: %q", got)
	}
	if Sparkline([]float64{math.NaN(), math.NaN()}) != "" {
		t.Error("all-NaN series should render empty")
	}
}
