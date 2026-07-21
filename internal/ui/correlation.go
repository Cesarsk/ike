package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// drillToLogs is the k9s killer feature: from a monitor, jump straight to
// its logs — the monitor's own query for log monitors, service:/env: tags
// otherwise. Esc pops back to the monitors view via the navigation stack.
func (a *App) drillToLogs(r data.Row) {
	query := r.LogQuery
	if query == "" && r.TraceID != "" {
		query = "trace_id:" + r.TraceID // from a trace/span with no derived query
	}
	if query == "" {
		a.flash("no log query derivable (no monitor scope / service tags / trace_id)", true)
		return
	}
	var logsRes data.Resource
	for _, res := range data.Resources() {
		if res.Key == "logs" {
			logsRes = res
		}
	}
	slog.Info("drill-down →logs", "from", a.res.Key, "id", r.ID, "query", query)
	a.queries["logs"] = query
	a.switchResource(logsRes) // pushes the current view; esc returns here
}

// drillToTrace opens the distributed-trace waterfall for a log or span row.
// The correlation hinges on trace_id: a log with none can't be correlated.
func (a *App) drillToTrace(r data.Row) {
	if r.TraceID == "" {
		a.flash("no trace_id on this row — is APM log-injection enabled for this service?", true)
		return
	}
	a.pushNav()
	a.detailRow = r
	a.loadTrace(r.TraceID)
}

// drillToServiceTraces is enter on a Services row: open the traces view scoped
// to that service (service:<name>) — the services → traces → logs loop.
func (a *App) drillToServiceTraces(r data.Row) {
	tr, ok := data.ResourceByAlias("traces")
	if !ok {
		return
	}
	a.queries["traces"] = "service:" + r.ID
	a.switchResource(tr) // pushes nav, so esc returns to :services
}

// showPatterns clusters the currently-loaded log messages into templates
// (zero-API — over the loaded sample, not the full window) as a triage aid.
func (a *App) showPatterns() {
	msgs := make([]string, 0, len(a.rows))
	for _, r := range a.rows {
		if len(r.Cells) > 4 { // MESSAGE column
			msgs = append(msgs, r.Cells[4])
		}
	}
	pats := data.ClusterLogs(msgs)
	a.pushNav()
	a.patterns.SetText(renderPatterns(pats, len(msgs))).ScrollToBeginning()
	a.patterns.SetTitle(fmt.Sprintf(" Log patterns [%d] ", len(pats)))
	a.showPage("patterns")
}

// renderPatterns lists clusters most-frequent first: count, template, example.
func renderPatterns(pats []data.LogPattern, sampled int) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]%d patterns[-:-:-] [gray]over %d loaded log lines · <esc> back[-]\n", len(pats), sampled)
	b.WriteString(" [gray]patterns are computed over the loaded sample, not the full window[-]\n\n")
	if len(pats) == 0 {
		b.WriteString(" [gray]no log lines loaded — run a query in the Logs view first[-]\n")
		return b.String()
	}
	maxN := pats[0].Count
	for _, p := range pats {
		bar := strings.Repeat("█", 1+p.Count*18/max(1, maxN))
		fmt.Fprintf(&b, " [white::b]%4d[-:-:-] [green]%s[-]\n      %s\n",
			p.Count, bar, tview.Escape(clip(p.Template, 110)))
	}
	return b.String()
}

// loadTrace fetches and renders the trace waterfall (on-demand, bounded).
func (a *App) loadTrace(traceID string) {
	a.trace.SetTitle(fmt.Sprintf(" Trace/%s ", traceID))
	a.trace.SetText("\n  [gray]reconstructing trace…").ScrollToBeginning()
	a.showPage("trace")
	prov := a.providerFor(a.detailRow) // the drilled-from row's org
	go func() {
		start := time.Now()
		v, err := prov.Trace(context.Background(), traceID)
		slog.Debug("trace render", "id", traceID, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			if a.page != "trace" || a.detailRow.TraceID != traceID {
				return // navigated away
			}
			if err != nil {
				a.trace.SetText("\n  [red]✗ " + tview.Escape(err.Error()))
				return
			}
			a.trace.SetText(renderTrace(v))
		})
	}()
}

// showLogContext opens the surrounding-context panel for a log row: the events
// in a ±window around it, same service/host, oldest first, as a navigable
// table. One search call, no polling — the cheap half of live tail.
func (a *App) showLogContext(r data.Row) {
	a.pushNav()
	a.detailRow = r
	anchorID := r.ID
	a.logCtxRows = nil
	a.logCtxCap.SetText(a.theme.recolor(" [orange::b]surrounding context[-:-:-] [gray]fetching…[-]"))
	a.logCtxTbl.Clear()
	a.logCtxFlex.SetTitle(" Log context ")
	a.showPage("logcontext")
	prov := a.providerFor(r) // the drilled-from row's org
	go func() {
		v, err := prov.LogContext(context.Background(), r, logContextWindowSecs)
		a.QueueUpdateDraw(func() {
			if a.page != "logcontext" || a.detailRow.ID != anchorID {
				return // navigated away
			}
			if err != nil {
				a.logCtxCap.SetText(a.theme.recolor(" [red]✗ " + tview.Escape(err.Error())))
				return
			}
			a.fillLogContext(v)
		})
	}()
}

// fillLogContext populates the context table and caption, selecting the anchor.
func (a *App) fillLogContext(v *data.LogContextView) {
	a.logCtxCap.SetText(a.theme.recolor(fmt.Sprintf(
		" [orange::b]surrounding context[-:-:-] [gray]%s · ±%s · %d lines · <enter> expand · <t> trace · <esc> back\n one query, no polling — a bounded window, not a live stream[-]",
		tview.Escape(scopeLabel(v)), v.Window.Round(time.Minute), len(v.Rows))))
	a.logCtxFlex.SetTitle(fmt.Sprintf(" Log context · %s [%d] ", scopeLabel(v), len(v.Rows)))

	a.logCtxTbl.Clear()
	for c, h := range []string{"", "TIME", "LVL", "SERVICE", "HOST", "MESSAGE"} {
		a.logCtxTbl.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(tcell.ColorWhite).SetAttributes(tcell.AttrBold).SetSelectable(false))
	}
	a.logCtxRows = v.Rows
	anchorTableRow := 1
	for i, r := range v.Rows {
		marker := ""
		if r.ID == v.AnchorID {
			marker = "▶"
			anchorTableRow = i + 1
		}
		cells := []string{marker, cellAt(r, 0), cellAt(r, 1), cellAt(r, 2), cellAt(r, 3), clip(cellAt(r, 4), 200)}
		for c, val := range cells {
			cell := tview.NewTableCell(tview.Escape(val)).SetExpansion(boolToInt(c == 5))
			if c == 2 {
				cell.SetTextColor(statusColor(val))
			}
			a.logCtxTbl.SetCell(i+1, c, cell)
		}
	}
	if len(v.Rows) == 0 {
		a.logCtxTbl.SetCell(1, 0, tview.NewTableCell(" no other log lines in this window").SetSelectable(false))
		return
	}
	a.logCtxTbl.Select(anchorTableRow, 0).ScrollToBeginning()
}

// logCtxSelected returns the log row under the cursor in the context table.
func (a *App) logCtxSelected() (data.Row, bool) {
	row, _ := a.logCtxTbl.GetSelection()
	i := row - 1 // header is table row 0
	if i < 0 || i >= len(a.logCtxRows) {
		return data.Row{}, false
	}
	return a.logCtxRows[i], true
}

// expandLogCtx opens the full detail for the selected context line (enter).
// Log rows are already the complete object, so this is a local render.
func (a *App) expandLogCtx() {
	r, ok := a.logCtxSelected()
	if !ok {
		return
	}
	a.pushNav()
	a.detailRow = r
	a.renderDetail(r)
	a.showPage("detail")
}

// scopeLabel names what the context query was scoped to for the panel title.
func scopeLabel(v *data.LogContextView) string {
	switch {
	case v.Service != "" && v.Host != "":
		return v.Service + "@" + v.Host
	case v.Service != "":
		return v.Service
	case v.Host != "":
		return v.Host
	}
	return "all services"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// statusColor maps a log status token to its severity colour (never themed —
// a warn must read yellow in every palette).
func statusColor(s string) tcell.Color {
	switch strings.ToLower(s) {
	case "error", "critical", "alert", "emergency":
		return tcell.ColorRed
	case "warn", "warning":
		return tcell.ColorYellow
	default:
		return tcell.ColorGray
	}
}

// renderTrace draws a span waterfall: each span indented by tree depth, with
// a proportional offset+duration bar, service:resource, and error marker.
func renderTrace(v *data.TraceView) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]trace %s[-:-:-]\n", tview.Escape(v.TraceID))
	fmt.Fprintf(&b, " [gray]%d spans · total %s · <l> logs for this trace · <esc> back[-]\n\n",
		len(v.Spans), data.FormatDuration(v.TotalUs))
	if len(v.Spans) == 0 {
		b.WriteString(" [gray]no spans found for this trace (retention/indexing, or wrong window)[-]\n")
		return b.String()
	}
	const barW, labelW = 40, 44
	for _, s := range v.Spans {
		indent := strings.Repeat("  ", s.Depth)
		// Compose the label from escaped user text + intentional color tags,
		// clipping the resource to fit; width is measured tag-free.
		label := tview.Escape(clip(indent+s.Service, labelW))
		if s.Resource != "" && visibleLen(label) < labelW-2 {
			room := labelW - visibleLen(label) - 1
			label += " [gray]" + tview.Escape(clip(s.Resource, room)) + "[-]"
		}
		bar := "[green]"
		if s.Error {
			bar = "[red]"
		}
		bar += traceBar(s.OffsetUs, s.DurationUs, v.TotalUs, barW) + "[-]"
		errTag := ""
		if s.Error {
			errTag = " [red]✗[-]"
		}
		fmt.Fprintf(&b, " %s%s %s [white]%s[-]%s\n",
			label, padVisible("", max(0, labelW-visibleLen(label))),
			bar, data.FormatDuration(s.DurationUs), errTag)
	}
	if v.Truncated {
		fmt.Fprintf(&b, "\n [yellow]trace truncated at %d spans[-]\n", 100)
	}

	// Unified request timeline: every service's logs for this trace, oldest
	// first — read the request story top-to-bottom across services.
	b.WriteString("\n [orange::b]logs in this trace (chronological, all services)[-:-:-]\n")
	if len(v.Logs) == 0 {
		b.WriteString(" [gray]no correlated logs — logs in this window may not carry trace_id[-]\n")
	}
	for _, lg := range v.Logs {
		statusColor := "[gray]"
		switch strings.ToLower(lg.Status) {
		case "error", "critical", "emergency":
			statusColor = "[red]"
		case "warn", "warning":
			statusColor = "[yellow]"
		case "info":
			statusColor = "[green]"
		}
		fmt.Fprintf(&b, " [gray]%s[-] %s%-5s[-] [aqua]%s[-] %s\n",
			lg.Time.Local().Format("15:04:05.000"), statusColor, clip(lg.Status, 5),
			tview.Escape(clip(lg.Service, 20)), tview.Escape(clip(lg.Message, 80)))
	}
	return b.String()
}

// traceBar renders a span's position/length within the trace as leading
// spaces (offset) + block glyphs (duration), scaled to width w.
func traceBar(offsetUs, durUs, totalUs int64, w int) string {
	if totalUs <= 0 {
		return strings.Repeat("█", 1)
	}
	lead := int(offsetUs * int64(w) / totalUs)
	length := int(durUs * int64(w) / totalUs)
	if length < 1 {
		length = 1
	}
	if lead+length > w {
		lead = w - length
	}
	if lead < 0 {
		lead = 0
	}
	return strings.Repeat("·", lead) + strings.Repeat("█", length)
}
