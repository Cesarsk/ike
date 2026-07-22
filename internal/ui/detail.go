package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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
	go func() {
		start := time.Now()
		view, err := a.providerFor(r).Dashboard(context.Background(), r.ID)
		slog.Debug("dashboard render", "id", r.ID, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			if a.page != "dashboard" || a.detailRow.ID != r.ID {
				return // navigated away
			}
			if err != nil {
				a.dash.SetText("\n  [red]✗ " + tview.Escape(err.Error()))
				return
			}
			a.dash.SetText(renderDashboard(view))
			if force {
				a.flash("sparklines refreshed", false)
			}
		})
	}()
}

// renderDashboard turns a DashboardView into the terminal panel. When the
// dashboard has layout coordinates it renders a grid (widgets side by side,
// in Datadog reading order); otherwise it falls back to a one-per-line list.
func renderDashboard(v *data.DashboardView) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]%s[-:-:-]\n", tview.Escape(v.Title))
	fmt.Fprintf(&b, " [gray]%d widgets · sparklines cover the last 1h · <ctrl-r> to refresh[-]\n\n", len(v.Widgets))

	hasCoords := false
	for _, w := range v.Widgets {
		if w.W > 0 {
			hasCoords = true
			break
		}
	}

	if !hasCoords {
		for _, w := range v.Widgets {
			b.WriteString(widgetLines(w, 0))
			b.WriteString("\n")
		}
	} else {
		ws := make([]data.Widget, len(v.Widgets))
		copy(ws, v.Widgets)
		sort.SliceStable(ws, func(i, j int) bool {
			if ws[i].Y != ws[j].Y {
				return ws[i].Y < ws[j].Y
			}
			return ws[i].X < ws[j].X
		})
		// Greedily pack widgets into rows by the 12-unit grid width.
		var row []data.Widget
		units := 0
		flush := func() {
			if len(row) > 0 {
				b.WriteString(renderWidgetRow(row))
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
	return b.String()
}

// renderWidgetRow lays out a row of widgets side by side in equal-width
// terminal columns, zipping their lines together with tag-aware padding.
func renderWidgetRow(row []data.Widget) string {
	const rowWidth = 96
	cellW := rowWidth/len(row) - 2
	if cellW < 18 {
		cellW = 18
	}
	cells := make([][]string, len(row))
	maxLines := 0
	for i, w := range row {
		cells[i] = strings.Split(strings.TrimRight(widgetLines(w, cellW), "\n"), "\n")
		if len(cells[i]) > maxLines {
			maxLines = len(cells[i])
		}
	}
	var b strings.Builder
	for ln := 0; ln < maxLines; ln++ {
		b.WriteString(" ")
		for i := range cells {
			cell := ""
			if ln < len(cells[i]) {
				cell = cells[i][ln]
			}
			b.WriteString(padVisible(cell, cellW))
			b.WriteString("  ")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// widgetLines renders one widget as title / sparkline+value (or note) /
// query. width>0 truncates the sparkline and query to fit a grid cell.
func widgetLines(w data.Widget, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[aqua::b]%s[-:-:-] [gray]%s[-]\n", tview.Escape(clip(w.Title, width)), tview.Escape(w.Type))
	switch {
	case w.HasData:
		spark := data.Sparkline(w.Spark)
		if width > 10 && len(spark) > width-10 {
			spark = spark[len(spark)-(width-10):] // keep the most recent points
		}
		fmt.Fprintf(&b, "[green]%s[-] [white::b]%s[-:-:-]\n", spark, data.FormatValue(w.Last))
	case w.Note != "":
		fmt.Fprintf(&b, "[gray]· %s[-]\n", tview.Escape(clip(w.Note, width)))
	}
	if w.Query != "" {
		fmt.Fprintf(&b, "[darkcyan]%s[-]\n", tview.Escape(clip(w.Query, width)))
	}
	return b.String()
}
