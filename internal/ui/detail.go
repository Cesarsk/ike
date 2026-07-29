package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

func (a *App) openDetail(tableRow int) {
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	_ = tableRow
	if a.res.Key == ctxResource.Key {
		a.switchContext(r.ID) // enter on a context switches org, k9s-style
		return
	}
	if a.res.Key == menuResource.Key {
		a.execCommand(r.ID) // enter on a command runs it (palette behavior)
		return
	}
	if a.res.Key == "dashboards" {
		a.pushNav()
		a.detailRow = r
		a.loadDashboard(r, false) // render widgets + sparklines instead of JSON
		return
	}
	if a.res.Key == "services" {
		a.drillToServiceTraces(r) // enter on a service → its traces (the loop)
		return
	}
	if a.res.Key == "oncall" {
		a.showTeamOnCall(r) // enter on a team → who's on call + escalation
		return
	}
	if a.res.Key == "teams" {
		a.showTeamMembers(r) // enter on a team → its members + roles
		return
	}
	if a.res.Key == "notebooks" {
		a.showNotebook(r) // enter on a notebook → its rendered cells
		return
	}
	a.pushNav()
	a.renderDetail(r)
	a.detailRow = r
	a.showPage("detail")

	// The list row is often only a summary (dashboards have no widgets in
	// the listing) — upgrade to the full object on demand, in background,
	// and swap it in if the user is still looking at this row.
	// Overview rows resolve to their underlying resource kind so the detail
	// (incident People header, monitor sparkline) matches the real object.
	detKey := a.res.Key
	if detKey == overviewResource.Key {
		if raw, ok := r.Raw.(map[string]any); ok {
			if k, _ := raw["kind"].(string); k != "" {
				detKey = k
			}
		}
	}
	res := a.res
	go func() {
		full, err := a.providerFor(r).FetchDetail(context.Background(), detKey, r.ID)
		if err != nil {
			slog.Warn("detail fetch failed", "resource", res.Key, "id", r.ID, "err", err)
			a.QueueUpdateDraw(func() { a.flash("✗ full object: "+err.Error(), true) })
			return
		}
		if full == nil {
			return // the row already was the complete object
		}
		slog.Debug("detail fetched", "resource", res.Key, "id", r.ID)
		var body string
		switch detKey {
		case "slos":
			if d, ok := full.(*data.SLODetail); ok {
				body = sloDetailBody(d)
			} else {
				body = jsonIndent(full)
			}
		case "synthetics":
			if d, ok := full.(*data.SynthDetail); ok {
				body = synthDetailBody(r, d)
			} else {
				body = jsonIndent(full)
			}
		case "incidents":
			// The war room: structured summary, people, impacts and to-dos in
			// one screen, with the raw object at the bottom for completeness.
			if d, ok := full.(*data.IncidentDetail); ok {
				prov := a.providerFor(r)
				todos, tErr := prov.IncidentTodos(context.Background(), r.ID)
				impacts, iErr := prov.IncidentImpacts(context.Background(), r.ID)
				if tErr != nil {
					slog.Warn("war room: to-dos unavailable", "id", r.ID, "err", tErr)
				}
				if iErr != nil {
					slog.Warn("war room: impacts unavailable", "id", r.ID, "err", iErr)
				}
				body = warRoomBody(r.ID, d, impacts, todos) + jsonIndent(d.Incident)
			} else {
				body = jsonIndent(full)
			}
		case "monitors":
			// Structured header + the evaluated metric sparkline — the data
			// behind the alert, so the detail answers "why is it firing?".
			if d, ok := full.(*data.MonitorDetail); ok {
				body = monitorDetailBody(d) + jsonIndent(d.Monitor)
			} else {
				body = jsonIndent(full)
			}
			if ms, mErr := a.providerFor(r).MonitorMetric(context.Background(), r.ID); mErr == nil {
				body = monitorMetricHeader(ms) + body
			}
		default:
			body = jsonIndent(full)
		}
		a.QueueUpdateDraw(func() {
			if a.page != "detail" || a.detailRow.ID != r.ID {
				return // user navigated away meanwhile
			}
			row, col := a.detail.GetScrollOffset()
			a.detail.SetText(body)
			a.detail.ScrollTo(row, col)
			a.flash("full object loaded", false)
		})
	}()
}

// monitorMetricHeader renders the metric sparkline block shown above a
// monitor's JSON. The detail view has dynamic colours OFF (so raw JSON
// renders safely), hence this is plain text — no colour tags.
func monitorMetricHeader(ms *data.MetricSeries) string {
	var b strings.Builder
	b.WriteString("── metric (last 1h) ──\n")
	if ms.Query != "" {
		b.WriteString(ms.Query + "\n")
	}
	if len(ms.Points) > 0 {
		fmt.Fprintf(&b, "%s  last %s\n", data.Sparkline(ms.Points), data.FormatValue(ms.Last))
	} else if ms.Note != "" {
		b.WriteString(ms.Note + "\n")
	}
	b.WriteString("──────────────────────\n\n")
	return b.String()
}

// jsonIndent renders a value as indented JSON for the detail view (dynamic
// colours are OFF there, so raw JSON is safe), surfacing any marshal error.
func jsonIndent(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "✗ " + err.Error()
	}
	return string(b)
}

// warRoomBody renders the incident war room: identity line, summary, people,
// impacts, to-dos and non-empty fields — plain text (the detail view has
// dynamic colours off), sections in triage order.
func warRoomBody(id string, d *data.IncidentDetail, impacts []string, todos []data.Todo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s · %s · %s ━━\n", id, d.Severity, d.State)
	if d.Title != "" {
		b.WriteString("  " + d.Title + "\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %-13s%s\n", "created:", d.Created)
	impact := "no"
	if d.CustomerImpacted {
		impact = "YES"
		if d.ImpactScope != "" {
			impact += " · " + d.ImpactScope
		}
	}
	fmt.Fprintf(&b, "  %-13s%s\n\n", "customer:", impact)

	b.WriteString(incidentPeopleHeader(d.People))

	b.WriteString("── impacts ──\n")
	if len(impacts) == 0 {
		b.WriteString("  (none declared)\n")
	}
	for _, im := range impacts {
		b.WriteString("  • " + im + "\n")
	}
	b.WriteString("\n── to-dos ──\n")
	if len(todos) == 0 {
		b.WriteString("  (none — press esc, then T for the to-do panel)\n")
	}
	for _, t := range todos {
		mark := "[ ]"
		if t.Completed {
			mark = "[x]"
		}
		line := "  " + mark + " " + t.Content
		if len(t.Assignees) > 0 {
			line += "   @" + strings.Join(t.Assignees, " @")
		}
		b.WriteString(line + "\n")
	}
	if len(d.Fields) > 0 {
		b.WriteString("\n── fields ──\n")
		keys := make([]string, 0, len(d.Fields))
		for k := range d.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-13s%s\n", k+":", d.Fields[k])
		}
	}
	b.WriteString("\n── raw ──\n")
	return b.String()
}

// monitorDetailBody renders the structured monitor header: identity, config
// and the alert message (runbook links live there), above the raw object.
func monitorDetailBody(d *data.MonitorDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s ━━\n\n", d.Name)
	fmt.Fprintf(&b, "  %-10s%s", "state:", d.State)
	if d.Priority != "" {
		fmt.Fprintf(&b, " · %s", d.Priority)
	}
	fmt.Fprintf(&b, " · %s\n", d.Type)
	if d.Query != "" {
		fmt.Fprintf(&b, "  %-10s%s\n", "query:", d.Query)
	}
	if len(d.Tags) > 0 {
		fmt.Fprintf(&b, "  %-10s%s\n", "tags:", strings.Join(d.Tags, " "))
	}
	if d.Message != "" {
		b.WriteString("\n── message ──\n")
		for _, line := range strings.Split(strings.TrimSpace(d.Message), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n── raw ──\n")
	return b.String()
}

// synthDetailBody renders a synthetic test's latest results: pass rate on
// top, then one line per recent run (when, from where, PASS/FAIL).
func synthDetailBody(r data.Row, d *data.SynthDetail) string {
	var b strings.Builder
	name := d.Name
	if name == "" && len(r.Cells) > 1 {
		name = r.Cells[1]
	}
	fmt.Fprintf(&b, "━━ %s ━━\n\n", name)
	if d.Note != "" {
		b.WriteString("  " + d.Note + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %-12s%.1f%% over the last %d runs\n\n", "pass rate:", d.PassRatePct, len(d.Results))
	b.WriteString("── latest results ──\n")
	for _, res := range d.Results {
		verdict := "PASS"
		if !res.Passed {
			verdict = "FAIL"
		}
		fmt.Fprintf(&b, "  %s  %-22s %s\n", res.CheckTime, res.Location, verdict)
	}
	return b.String()
}

// sloDetailBody renders the structured SLO detail: config, attainment, error
// budget, burn rate and the budget burndown sparkline (plain text — the
// detail view has dynamic colours off).
func sloDetailBody(d *data.SLODetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s ━━\n\n", d.Name)
	fmt.Fprintf(&b, "  %-13s%s\n", "type:", d.Type)
	fmt.Fprintf(&b, "  %-13s%.2f%% over %dd\n", "target:", d.TargetPct, d.TimeframeDays)
	fmt.Fprintf(&b, "  %-13s%.3f%%\n", "attainment:", d.AttainmentPct)
	fmt.Fprintf(&b, "  %-13s%.1f%%\n", "budget left:", d.BudgetRemainingPct)
	if d.BurnRate > 0 {
		verdict := "sustainable"
		if d.BurnRate > 1 {
			verdict = "ON TRACK TO BREACH"
		}
		fmt.Fprintf(&b, "  %-13s%.2fx (%s)\n", "burn rate:", d.BurnRate, verdict)
	}
	b.WriteString("\n── error-budget burndown ──\n")
	if len(d.Burndown) > 1 {
		fmt.Fprintf(&b, "  %s  now %.1f%%\n", data.Sparkline(d.Burndown), d.Burndown[len(d.Burndown)-1])
		fmt.Fprintf(&b, "  window: last %dd, oldest → newest\n", d.TimeframeDays)
	} else if d.Note != "" {
		b.WriteString("  " + d.Note + "\n")
	} else {
		b.WriteString("  (no series)\n")
	}
	return b.String()
}

// incidentPeopleHeader renders the resolved People block shown above an
// incident's JSON. Plain text (no colour tags — the detail view has dynamic
// colours off). Responders are read-only (the API has no write path); an
// unresolved responder shows its raw id.
func incidentPeopleHeader(p data.IncidentPeople) string {
	dash := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	responders := "—"
	if len(p.Responders) > 0 {
		responders = strings.Join(p.Responders, ", ")
	}
	var b strings.Builder
	b.WriteString("── people ──\n")
	fmt.Fprintf(&b, "  %-13s%s\n", "commander:", dash(p.Commander))
	fmt.Fprintf(&b, "  %-13s%s\n", "responders:", responders)
	fmt.Fprintf(&b, "  %-13s%s\n", "declared by:", dash(p.DeclaredBy))
	fmt.Fprintf(&b, "  %-13s%s\n", "created by:", dash(p.CreatedBy))
	b.WriteString("────────────\n\n")
	return b.String()
}

func (a *App) renderDetail(r data.Row) {
	b, err := json.MarshalIndent(r.Raw, "", "  ")
	if err != nil {
		b = []byte("✗ " + err.Error())
	}
	a.detail.SetText(string(b)).ScrollToBeginning()
	a.detail.SetTitle(fmt.Sprintf(" %s/%s ", strings.TrimSuffix(a.res.Title, "s"), r.ID))
}

// dashRanges are the dashboard time windows on digit keys 1-5 — the same
// mapping as the logs/traces views, defaulting to the web UI's 1h.
var dashRanges = []struct {
	label  string
	window time.Duration
}{
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
	{"4h", 4 * time.Hour},
	{"1d", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
	{"1mo", 30 * 24 * time.Hour},
}

// setDashRange picks a preset window by digit index.
func (a *App) setDashRange(ix int) {
	if ix < 0 || ix >= len(dashRanges) {
		return
	}
	a.setDashWindow(dashRanges[ix].label, dashRanges[ix].window)
}

// setDashWindow switches the dashboard window (preset or custom) and
// re-fetches — a new window is new data, the same deliberate spend as the
// web picker.
func (a *App) setDashWindow(label string, d time.Duration) {
	if d <= 0 || d == a.dashWindow {
		return
	}
	a.dashWindow, a.dashLabel = d, label
	a.flash("window → "+label, false)
	a.loadDashboard(a.detailRow, true)
}

// parseWindow reads a human window: Go durations (30m, 90s, 4h) plus the
// dashboard-native d / w / mo suffixes.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	for suffix, unit := range map[string]time.Duration{
		"mo": 30 * 24 * time.Hour,
		"w":  7 * 24 * time.Hour,
		"d":  24 * time.Hour,
	} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.Atoi(strings.TrimSpace(n))
			if err != nil || v <= 0 {
				return 0, fmt.Errorf("bad window %q", s)
			}
			return time.Duration(v) * unit, nil
		}
	}
	return time.ParseDuration(s)
}

// loadDashboard renders a dashboard's widgets with sparklines. Fetch is
// on-demand and bounded (see data.maxDashWidgets); force=true is ctrl-r.
func (a *App) loadDashboard(r data.Row, force bool) {
	a.dash.SetTitle(fmt.Sprintf(" Dashboard/%s ", r.ID))
	if !force {
		a.dash.SetText("\n  [gray]loading widgets…").ScrollToBeginning()
	} else {
		a.flash("refreshing sparklines…", false)
	}
	a.showPage("dashboard")
	window, label := a.dashWindow, a.dashLabel
	go func() {
		start := time.Now()
		view, err := a.providerFor(r).Dashboard(context.Background(), r.ID, window)
		slog.Debug("dashboard render", "id", r.ID, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			if a.page != "dashboard" || a.detailRow.ID != r.ID {
				return // navigated away
			}
			if err != nil {
				a.dash.SetText("\n  [red]✗ " + tview.Escape(err.Error()))
				return
			}
			_, _, paneW, _ := a.dash.GetInnerRect()
			text, order := renderDashboard(view, label, paneW)
			a.dashViewData, a.dashWidgets, a.dashSel, a.dashZoom = view, order, 0, false
			a.dash.SetText(text)
			a.dashHighlight()
			if force {
				a.flash("widgets refreshed", false)
			}
		})
	}()
}

// renderDashboard turns a DashboardView into the terminal panel. When the
// dashboard has layout coordinates it renders a grid (widgets side by side,
// in Datadog reading order); otherwise it falls back to a one-per-line list.
func renderDashboard(v *data.DashboardView, rangeLabel string, paneWidth int) (string, []data.Widget) {
	// Fill the terminal the user actually has, instead of cramming into a
	// fixed column budget — the single biggest de-densifier on wide screens.
	rowWidth := paneWidth - 4
	if rowWidth < 60 {
		rowWidth = 60
	}
	rule := " [#444444]" + strings.Repeat("─", rowWidth) + "[-]\n"
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]%s[-:-:-]\n", tview.Escape(v.Title))
	fmt.Fprintf(&b, " [gray]%d widgets · last %s (<1>15m..<6>1mo, <w> custom) · <j/k> select · <enter> zoom · <ctrl-r> refresh[-]\n", len(v.Widgets), rangeLabel)
	b.WriteString(rule)
	b.WriteString("\n")

	hasCoords := false
	for _, w := range v.Widgets {
		if w.W > 0 {
			hasCoords = true
			break
		}
	}

	ws := make([]data.Widget, len(v.Widgets))
	copy(ws, v.Widgets)
	if !hasCoords {
		for i, w := range ws {
			b.WriteString(widgetLines(w, 0, i))
			if i < len(ws)-1 {
				b.WriteString(rule)
			}
			b.WriteString("\n")
		}
	} else {
		sort.SliceStable(ws, func(i, j int) bool {
			if ws[i].Y != ws[j].Y {
				return ws[i].Y < ws[j].Y
			}
			return ws[i].X < ws[j].X
		})
		// Greedily pack widgets into rows by the 12-unit grid width. The
		// region index passed to widgetLines is the display-order index.
		var row []data.Widget
		units, base := 0, 0
		flush := func() {
			if base > 0 {
				b.WriteString(rule) // dim rule between widget rows
			}
			if len(row) > 0 {
				b.WriteString(renderWidgetRow(row, base, rowWidth))
				base += len(row)
				row, units = nil, 0
			}
		}
		for _, w := range ws {
			wu := w.W
			if wu <= 0 {
				wu = dashGridCols
			}
			if units+wu > dashGridCols {
				flush()
			}
			row = append(row, w)
			units += wu
		}
		flush()
	}

	if v.Truncated {
		fmt.Fprintf(&b, " [yellow]Note: only the first %d metric widgets were charted to protect the API budget.[-]\n", data.MaxDashWidgets)
	}
	return b.String(), ws
}

// renderWidgetRow lays out a row of widgets side by side in equal-width
// terminal columns, zipping their lines together with tag-aware padding.
func renderWidgetRow(row []data.Widget, base, rowWidth int) string {
	// Cells share the row minus " │ " separators between them.
	sep := 3 * (len(row) - 1)
	cellW := (rowWidth - sep) / len(row)
	if cellW < 18 {
		cellW = 18
	}
	cells := make([][]string, len(row))
	maxLines := 0
	for i, w := range row {
		cells[i] = strings.Split(strings.TrimRight(widgetLines(w, cellW, base+i), "\n"), "\n")
		if len(cells[i]) > maxLines {
			maxLines = len(cells[i])
		}
	}
	var b strings.Builder
	for ln := 0; ln < maxLines; ln++ {
		b.WriteString(" ")
		for i := range cells {
			if i > 0 {
				b.WriteString(" [#444444]│[-] ") // dim cell boundary
			}
			cell := ""
			if ln < len(cells[i]) {
				cell = cells[i][ln]
			}
			b.WriteString(padVisible(cell, cellW))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// widgetLines renders one widget by its Datadog type: query_value as a big
// single value with its 1h trend, toplist as ranked horizontal bars, anything
// else with data as a sparkline. width>0 truncates content to fit a grid cell.
func widgetLines(w data.Widget, width int, idx int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[\"w%d\"][aqua::b]%s[-:-:-][\"\"] [gray]%s[-]\n", idx, tview.Escape(clip(w.Title, width)), tview.Escape(w.Type))
	switch {
	case w.HasData && w.Type == "slo":
		// SLO widgets: live attainment big, target/window/budget as the note.
		fmt.Fprintf(&b, "[white::b]  %.2f%%[-:-:-]\n", w.Last)
		if w.Note != "" {
			fmt.Fprintf(&b, "[gray]· %s[-]\n", tview.Escape(clip(w.Note, width)))
		}
	case w.HasData && w.Type == "query_value":
		// The single-value / gauge shape: the number is the point.
		fmt.Fprintf(&b, "[white::b]  %s[-:-:-]%s\n", data.FormatValue(w.Last), trendLabel(w.Spark))
	case w.HasData && w.Type == "toplist" && len(w.Items) > 0:
		b.WriteString(topListBars(w.Items, width))
	case w.HasData:
		// A real multi-row chart — one-line sparklines were the "too small"
		// complaint; height gives peaks and valleys actual shape.
		chartW := width - 2
		if width <= 0 {
			chartW = 60
		}
		for _, row := range data.ChartRows(w.Spark, chartW, 4) {
			fmt.Fprintf(&b, "[green]%s[-]\n", row)
		}
		fmt.Fprintf(&b, "[white::b]%s[-:-:-]%s%s\n", data.FormatValue(w.Last), trendLabel(w.Spark), seriesLabel(w.Series))
	case w.Note != "":
		fmt.Fprintf(&b, "[gray]· %s[-]\n", tview.Escape(clip(w.Note, width)))
	}
	if w.Query != "" {
		fmt.Fprintf(&b, "[darkcyan]%s[-]\n", tview.Escape(clip(w.Query, width)))
	}
	return b.String()
}

// dashHighlight moves the region highlight to the selected widget and keeps
// it in view.
func (a *App) dashHighlight() {
	if len(a.dashWidgets) == 0 {
		return
	}
	a.dash.Highlight(fmt.Sprintf("w%d", a.dashSel)).ScrollToHighlight()
}

// dashMove shifts the widget selection (grid mode) or steps between widgets
// while zoomed.
func (a *App) dashMove(delta int) {
	if len(a.dashWidgets) == 0 {
		return
	}
	a.dashSel += delta
	if a.dashSel < 0 {
		a.dashSel = 0
	}
	if a.dashSel >= len(a.dashWidgets) {
		a.dashSel = len(a.dashWidgets) - 1
	}
	if a.dashZoom {
		_, _, paneW, _ := a.dash.GetInnerRect()
		a.dash.SetText(renderWidgetZoom(a.dashWidgets[a.dashSel], a.dashSel, len(a.dashWidgets), paneW)).ScrollToBeginning()
		return
	}
	a.dashHighlight()
}

// dashZoomIn renders the selected widget full-pane: nothing truncated.
func (a *App) dashZoomIn() {
	if len(a.dashWidgets) == 0 {
		return
	}
	a.dashZoom = true
	_, _, paneW, _ := a.dash.GetInnerRect()
	a.dash.SetText(renderWidgetZoom(a.dashWidgets[a.dashSel], a.dashSel, len(a.dashWidgets), paneW)).ScrollToBeginning()
}

// dashZoomOut returns from the zoom to the grid (no re-fetch), selection kept.
func (a *App) dashZoomOut() {
	a.dashZoom = false
	if a.dashViewData == nil {
		return
	}
	_, _, paneW, _ := a.dash.GetInnerRect()
	text, order := renderDashboard(a.dashViewData, a.dashLabel, paneW)
	a.dashWidgets = order
	a.dash.SetText(text)
	a.dashHighlight()
}

// renderWidgetZoom is the full-pane view of one widget: complete title,
// query, stats, every toplist row — the readable version of a clipped cell.
func renderWidgetZoom(w data.Widget, idx, total, paneW int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n [orange::b]%s[-:-:-]  [gray]%s · widget %d/%d[-]\n\n", tview.Escape(w.Title), tview.Escape(w.Type), idx+1, total)
	// Headline first (stats, query), the tall chart last — so the facts are
	// readable without scrolling even on short panes.
	switch {
	case w.HasData && w.Type == "slo":
		fmt.Fprintf(&b, " [gray]attainment[-] [white::b]%.2f%%[-:-:-]\n", w.Last)
	case w.HasData && len(w.Items) == 0:
		min, max, sum, n := math.Inf(1), math.Inf(-1), 0.0, 0
		for _, p := range w.Spark {
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
			fmt.Fprintf(&b, " [gray]last[-] [white::b]%s[-:-:-]   [gray]min[-] %s   [gray]avg[-] %s   [gray]max[-] %s%s%s\n",
				data.FormatValue(w.Last), data.FormatValue(min), data.FormatValue(sum/float64(n)), data.FormatValue(max), trendLabel(w.Spark), seriesLabel(w.Series))
		}
	}
	if w.Note != "" {
		fmt.Fprintf(&b, " %s\n", tview.Escape(w.Note))
	}
	if w.Query != "" {
		fmt.Fprintf(&b, " [gray]query:[-] [darkcyan]%s[-]\n", tview.Escape(w.Query))
	}
	switch {
	case w.HasData && len(w.Items) > 0:
		b.WriteString("\n")
		b.WriteString(topListBars(w.Items, 0))
	case w.HasData && len(w.Spark) > 0:
		chartW := paneW - 4
		if chartW < 40 || chartW > 160 {
			chartW = 100
		}
		b.WriteString("\n")
		if w.Type == "slo" {
			b.WriteString(" [gray]error budget remaining %[-]\n")
		}
		for _, row := range data.ChartRows(w.Spark, chartW, 10) {
			fmt.Fprintf(&b, " [green]%s[-]\n", row)
		}
	}
	b.WriteString("\n [gray]<j/k> prev/next widget · <esc> back to the grid[-]\n")
	return b.String()
}

// trendLabel summarises a series as its change over the window (▲/▼ percent),
// blank when flat or unknowable. NaN gaps are skipped at both ends.
func trendLabel(spark []float64) string {
	first, last := math.NaN(), math.NaN()
	for _, p := range spark {
		if !math.IsNaN(p) {
			first = p
			break
		}
	}
	if last, _ = data.LastValid(spark); math.IsNaN(first) || first == 0 {
		return ""
	}
	pct := (last - first) / first * 100
	switch {
	case pct >= 1:
		return fmt.Sprintf("  [gray]▲ %.0f%%[-]", pct)
	case pct <= -1:
		return fmt.Sprintf("  [gray]▼ %.0f%%[-]", -pct)
	}
	return ""
}

// seriesLabel marks a chart that summarises several series (per-bucket max).
func seriesLabel(n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf("  [gray](max of %d series)[-]", n)
}

// topListBars draws a toplist's groups as labelled horizontal bars scaled to
// the largest value.
func topListBars(items []data.WidgetItem, width int) string {
	barW := 16
	labelW := 14
	switch {
	case width == 0:
		// Zoom / unbounded: show the whole label — readability is the point.
		barW, labelW = 24, 0
		for _, it := range items {
			if n := len([]rune(it.Label)); n > labelW {
				labelW = n
			}
		}
		if labelW > 64 {
			labelW = 64
		}
	case width < 44:
		barW, labelW = 8, 10
	}
	max := items[0].Value
	for _, it := range items {
		if it.Value > max {
			max = it.Value
		}
	}
	var b strings.Builder
	for _, it := range items {
		n := 0
		if max > 0 {
			n = int(it.Value / max * float64(barW))
		}
		if n < 1 && it.Value > 0 {
			n = 1
		}
		label := it.Label
		if r := []rune(label); len(r) > labelW {
			label = string(r[:labelW-1]) + "…"
		}
		fmt.Fprintf(&b, "[white]%-*s[-] [green]%s[-] %s\n",
			labelW, tview.Escape(label), strings.Repeat("▇", n), data.FormatValue(it.Value))
	}
	return b.String()
}
