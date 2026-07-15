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

	cases := []struct {
		name        string
		row         data.Row
		state, text string
		want        bool
	}{
		{"warn filter must not match OK row with 'Warning' in name", ok, "Warn", "", false},
		{"warn filter matches Warn state", warn, "Warn", "", true},
		{"state match is case-insensitive", warn, "warn", "", true},
		{"no filters matches everything", ok, "", "", true},
		{"text filter still searches all cells", ok, "", "warning", true},
		{"state and text combine with AND", alert, "Alert", "kong", true},
		{"state and text: state mismatch loses", ok, "Alert", "warning", false},
		{"state and text: text mismatch loses", alert, "Alert", "vault", false},
		{"empty row never matches a state filter", data.Row{}, "OK", "", false},
	}
	for _, c := range cases {
		if got := matchRow(c.row, c.state, c.text); got != c.want {
			t.Errorf("%s: matchRow(state=%q, text=%q) = %v, want %v", c.name, c.state, c.text, got, c.want)
		}
	}
}
