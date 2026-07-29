package ui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// openMetricsExplorer shows the :metrics page: a free-form metric query
// charted full-pane, with the dashboard time-range keys.
func (a *App) openMetricsExplorer() {
	if a.page == "metrics" {
		return
	}
	if a.res.Key != "" {
		a.pushNav()
	}
	a.showPage("metrics")
	if a.mxQuery == "" {
		a.mx.SetTitle(" Metrics ")
		a.mx.SetText(a.theme.recolor(metricsIntro)).ScrollToBeginning()
		return
	}
	a.loadMetrics()
}

const metricsIntro = `
 [orange::b]metrics explorer[-:-:-]

 Press [aqua]/[-] and type a metric query, the same syntax as the web UI:

   [darkcyan]avg:system.cpu.user{*}[-]
   [darkcyan]sum:kong.requests{env:prod}.as_rate()[-]
   [darkcyan]max:kubernetes.memory.usage{*} by {kube_cluster_name}[-]

 [gray]<1-6> window 15m..1mo · <w> custom · <ctrl-r> refresh · <esc> back[-]
`

// loadMetrics runs the current query over the current window.
func (a *App) loadMetrics() {
	query, window, label := a.mxQuery, a.mxWindow, a.mxLabel
	if window <= 0 {
		window, label = time.Hour, "1h"
	}
	a.mx.SetTitle(fmt.Sprintf(" Metrics · last %s ", label))
	a.mx.SetText(a.theme.recolor("\n  [gray]querying…")).ScrollToBeginning()
	prov := a.provider
	go func() {
		v, err := prov.MetricQuery(context.Background(), query, window)
		a.QueueUpdateDraw(func() {
			if a.page != "metrics" {
				return // navigated away
			}
			if err != nil {
				a.mx.SetText(a.theme.recolor("\n  [red]✗ " + tview.Escape(err.Error())))
				return
			}
			_, _, paneW, _ := a.mx.GetInnerRect()
			a.mx.SetText(a.theme.recolor(renderMetricsBody(v, label, paneW))).ScrollToBeginning()
		})
	}()
}

// setMxWindow applies a metrics-explorer window and re-queries.
func (a *App) setMxWindow(label string, d time.Duration) {
	a.mxWindow, a.mxLabel = d, label
	if a.mxQuery != "" {
		a.loadMetrics()
	}
}

// renderMetricsBody draws the explorer result: query, stats, tall chart and
// the per-series ranking for grouped queries.
func renderMetricsBody(v *data.MetricExplorer, label string, paneW int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n [darkcyan]%s[-]  [gray]last %s[-]\n\n", tview.Escape(v.Query), label)
	if v.Note != "" {
		fmt.Fprintf(&b, " [gray]%s[-]\n", tview.Escape(v.Note))
		return b.String()
	}
	min, max, sum, n := math.Inf(1), math.Inf(-1), 0.0, 0
	for _, p := range v.Spark {
		if math.IsNaN(p) {
			continue
		}
		if p < min {
			min = p
		}
		if p > max {
			max = p
		}
		sum += p
		n++
	}
	if n > 0 {
		fmt.Fprintf(&b, " [gray]last[-] [white::b]%s[-:-:-]   [gray]min[-] %s   [gray]avg[-] %s   [gray]max[-] %s%s\n\n",
			data.FormatValue(v.Last), data.FormatValue(min), data.FormatValue(sum/float64(n)), data.FormatValue(max), seriesLabel(v.Series))
	}
	// Facts above the fold: the ranked series before the tall chart.
	if len(v.Items) > 0 {
		b.WriteString(" [gray]series (last value)[-]\n")
		b.WriteString(topListBars(v.Items, 0))
		b.WriteString("\n")
	}
	chartW := paneW - 4
	if chartW < 40 || chartW > 160 {
		chartW = 100
	}
	for _, row := range data.ChartRows(v.Spark, chartW, 10) {
		fmt.Fprintf(&b, " [green]%s[-]\n", row)
	}
	b.WriteString("\n [gray]</> query · <1-6> window 15m..1mo · <w> custom · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}
