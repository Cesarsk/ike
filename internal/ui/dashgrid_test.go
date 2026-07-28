package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

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
	out, _ := renderDashboard(v, "1h", 100)
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
	out, _ := renderDashboard(v, "1h", 100)
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

func TestSparkTruncationIsRuneSafe(t *testing.T) {
	// The sparkline is 3-byte block runes; cell truncation must slice by
	// runes or the output starts with invalid UTF-8 ("�?" on screen).
	pts := make([]float64, 60)
	for i := range pts {
		pts[i] = float64(i % 7)
	}
	out := widgetLines(data.Widget{Title: "t", Type: "timeseries", HasData: true, Spark: pts, Last: 1}, 24, 0)
	if !utf8.ValidString(out) {
		t.Errorf("truncated widget output contains invalid UTF-8:\n%q", out)
	}
}

func TestToplistZoomShowsFullLabels(t *testing.T) {
	out := topListBars([]data.WidgetItem{{Label: "kube_service:very-long-service-name", Value: 5}}, 0)
	if !strings.Contains(out, "kube_service:very-long-service-name") {
		t.Errorf("zoom (width=0) should show the full label:\n%s", out)
	}
}

func TestWidgetTypeRendering(t *testing.T) {
	// query_value: the big single value + trend, no sparkline noise.
	qv := widgetLines(data.Widget{
		Title: "p99", Type: "query_value", HasData: true,
		Spark: []float64{100, 150}, Last: 150,
	}, 0, 0)
	if !strings.Contains(qv, "150") || !strings.Contains(qv, "▲ 50%") {
		t.Errorf("query_value should render the value + trend:\n%s", qv)
	}
	if strings.Contains(qv, "▁") || strings.Contains(qv, "█") {
		t.Errorf("query_value should not render a sparkline:\n%s", qv)
	}

	// toplist: ranked horizontal bars, scaled to the max.
	tl := widgetLines(data.Widget{
		Title: "Restarts", Type: "toplist", HasData: true,
		Spark: []float64{1}, Last: 1,
		Items: []data.WidgetItem{{Label: "kong-proxy", Value: 8}, {Label: "redis", Value: 2}},
	}, 0, 0)
	if !strings.Contains(tl, "kong-proxy") || !strings.Contains(tl, "▇") {
		t.Errorf("toplist should render bars:\n%s", tl)
	}
	kong, redis := 0, 0
	for _, line := range strings.Split(tl, "\n") {
		if strings.Contains(line, "kong-proxy") {
			kong = strings.Count(line, "▇")
		}
		if strings.Contains(line, "redis") {
			redis = strings.Count(line, "▇")
		}
	}
	if kong <= redis {
		t.Errorf("bars should scale with value (kong=%d redis=%d):\n%s", kong, redis, tl)
	}
}
