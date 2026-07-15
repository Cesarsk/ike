package ui

import (
	"strings"
	"testing"
)

func TestFormatBudget(t *testing.T) {
	if got := formatBudget(nil); !strings.Contains(got, "n/a") {
		t.Errorf("empty budget = %q", got)
	}

	// remaining/limit ratios drive the colour: >50% green, >20% yellow, ≤20% red.
	out := formatBudget([]string{
		"monitors 999/1000 per 10s",          // 99% → green
		"logs_public_search_api 2/3 per 10s", // 66% → green
		"slo_list 30/100 per 60s",            // 30% → yellow
		"incidents 1/100 per 60s",            // 1%  → red
	})
	for _, want := range []string{"[green]999/1000", "logs_search", "[yellow]30/100", "[red]1/100"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatBudget missing %q in:\n%s", want, out)
		}
	}
	// verbose api name is shortened
	if strings.Contains(out, "logs_public_search_api") {
		t.Errorf("name not shortened: %s", out)
	}
}
