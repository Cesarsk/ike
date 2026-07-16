package ui

import (
	"strings"
	"testing"

	"github.com/Cesarsk/ike/internal/data"
)

func TestRenderDashboardGrid(t *testing.T) {
	// Two width-6 widgets on row y=0 should render side by side (same line),
	// a width-12 widget on y=2 should occupy its own row.
	v := &data.DashboardView{
		Title: "Test",
		Widgets: []data.Widget{
			{Title: "Left", Type: "timeseries", X: 0, Y: 0, W: 6, HasData: true, Spark: []float64{1, 2, 3}, Last: 3},
			{Title: "Right", Type: "timeseries", X: 6, Y: 0, W: 6, HasData: true, Spark: []float64{3, 2, 1}, Last: 1},
			{Title: "Wide", Type: "timeseries", X: 0, Y: 2, W: 12, HasData: true, Spark: []float64{1, 1, 1}, Last: 1},
		},
	}
	out := renderDashboard(v)
	// Left and Right must appear on the same physical line (grid, not list).
	var sideBySide bool
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Left") && strings.Contains(line, "Right") {
			sideBySide = true
		}
	}
	if !sideBySide {
		t.Errorf("expected Left and Right on the same row (grid layout):\n%s", out)
	}
	if !strings.Contains(out, "Wide") {
		t.Error("wide widget missing")
	}
}

func TestRenderDashboardListFallback(t *testing.T) {
	// No layout coords → one-per-line list; must not panic and must include all.
	v := &data.DashboardView{Title: "Ordered", Widgets: []data.Widget{
		{Title: "A", Type: "note", Note: "n"},
		{Title: "B", Type: "timeseries", HasData: true, Spark: []float64{1, 2}, Last: 2},
	}}
	out := renderDashboard(v)
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("list fallback dropped widgets:\n%s", out)
	}
}

func TestVisibleLenIgnoresTags(t *testing.T) {
	if got := visibleLen("[green]abc[-]"); got != 3 {
		t.Errorf("visibleLen = %d, want 3", got)
	}
	if got := padVisible("[red]hi[-]", 5); visibleLen(got) != 5 {
		t.Errorf("padVisible visible width = %d, want 5", visibleLen(got))
	}
}

func TestClip(t *testing.T) {
	if got := clip("hello world", 5); got != "hell…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("hi", 0); got != "hi" {
		t.Errorf("clip no-limit = %q", got)
	}
}
