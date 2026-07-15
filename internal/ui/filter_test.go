package ui

import (
	"testing"

	"github.com/Cesarsk/ike/internal/data"
)

// Regression for the live-mode bug where the Warn quick filter matched every
// monitor named "… Log Index Warning Threshold Reached": the state quick
// filter must match the STATE column exactly, never other cells.
func TestMatchRow(t *testing.T) {
	ok := data.Row{Cells: []string{"OK", "dev - infrastructure - Log Index Warning Threshold Reached", "metric alert"}}
	warn := data.Row{Cells: []string{"Warn", "Vault sealed", "service check"}}
	alert := data.Row{Cells: []string{"Alert", "Kong data plane 5xx rate", "metric alert"}}

	// col 0 is STATE for monitors; -1 means no column filter.
	sloMetric := data.Row{Cells: []string{"Kong availability", "metric", "99.90%"}}
	sloMonitor := data.Row{Cells: []string{"EKS control plane", "monitor", "99.95%"}}
	cases := []struct {
		name string
		row  data.Row
		col  int
		val  string
		text string
		want bool
	}{
		{"warn filter must not match OK row with 'Warning' in name", ok, 0, "Warn", "", false},
		{"warn filter matches Warn state", warn, 0, "Warn", "", true},
		{"state match is case-insensitive", warn, 0, "warn", "", true},
		{"no filters matches everything", ok, -1, "", "", true},
		{"text filter still searches all cells", ok, -1, "", "warning", true},
		{"state and text combine with AND", alert, 0, "Alert", "kong", true},
		{"state and text: state mismatch loses", ok, 0, "Alert", "warning", false},
		{"state and text: text mismatch loses", alert, 0, "Alert", "vault", false},
		{"empty row never matches a column filter", data.Row{}, 0, "OK", "", false},
		{"SLO type filter matches TYPE column (1)", sloMetric, 1, "metric", "", true},
		{"SLO type filter on col 1 excludes monitor", sloMonitor, 1, "metric", "", false},
	}
	for _, c := range cases {
		if got := matchRow(c.row, c.col, c.val, c.text); got != c.want {
			t.Errorf("%s: matchRow(col=%d, val=%q, text=%q) = %v, want %v", c.name, c.col, c.val, c.text, got, c.want)
		}
	}
}
