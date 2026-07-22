package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

func cellAt(r data.Row, col int) string {
	if col < len(r.Cells) {
		return r.Cells[col]
	}
	return ""
}

func (a *App) render() {
	prevRow, _ := a.table.GetSelection()
	a.table.Clear()
	names, cidx := a.displayColumns()
	if a.spanning() {
		// Display-only CTX column, first, when >1 org is active. The sentinel
		// index -1 reads Row.Ctx at render time — Row.Cells are never touched,
		// so sorting, quick filters and positional reads are unaffected.
		names = append([]string{"CTX"}, names...)
		cidx = append([]int{-1}, cidx...)
	}
	for c, col := range names {
		cell := tview.NewTableCell(col).
			SetTextColor(tcell.ColorWhite).
			SetAttributes(tcell.AttrBold).
			SetSelectable(false).
			SetExpansion(expansion(col))
		a.table.SetCell(0, c, cell)
	}
	for n, idx := range a.filtered {
		r := a.rows[idx]
		color := rowColor(a.res.Key, r)
		// Active orgs in :ctx are a marked set — tint the whole row so a
		// multi-selection reads at a glance (the cursor bar stays distinct:
		// tview's selected style overrides cell styles on the selected row).
		marked := (a.res.Key == ctxResource.Key && len(r.Cells) > 0 && r.Cells[0] == "active") || a.marks[rowKey(r)]
		for c, ci := range cidx {
			val := ""
			switch {
			case ci == -1:
				val = r.Ctx
			case ci < len(r.Cells):
				val = r.Cells[ci]
			}
			if len(val) > 200 {
				val = val[:197] + "…"
			}
			cell := tview.NewTableCell(tview.Escape(val)).
				SetTextColor(color).
				SetExpansion(expansion(names[c]))
			if marked {
				cell.SetBackgroundColor(a.theme.MarkBg).SetAttributes(tcell.AttrBold)
			}
			a.table.SetCell(n+1, c, cell)
		}
	}
	// Loaded-but-empty: show the resource's hint so a blank table doesn't read
	// as broken. Gated on loadedKey so it never appears mid-switch or mid-fetch.
	if len(a.filtered) == 0 && a.loadedKey == a.res.Key && a.res.EmptyHint != "" {
		a.table.SetCell(1, 0, tview.NewTableCell("  "+a.res.EmptyHint).
			SetTextColor(tcell.ColorGray).SetSelectable(false).SetExpansion(1))
	}
	var parts []string
	if a.colFilterVal != "" && a.colFilterCol >= 0 && a.colFilterCol < len(a.res.Columns) {
		parts = append(parts, strings.ToLower(a.res.Columns[a.colFilterCol])+":"+a.colFilterVal)
	}
	if a.filter != "" {
		parts = append(parts, "/"+a.filter)
	}
	flabel := strings.Join(parts, " ")
	if flabel == "" {
		flabel = "all"
	}
	if a.res.ServerQuery {
		flabel = a.queries[a.res.Key]
	}
	if a.res.Key == "logs" {
		flabel = fmt.Sprintf("%s · %s", flabel, logRanges[a.logRangeIx].label)
	}
	sortLabel := ""
	if a.sortCol >= 0 && a.sortCol < len(a.res.Columns) {
		sortLabel = fmt.Sprintf(" ↕%s%s", a.res.Columns[a.sortCol], arrow(a.sortAsc))
	}
	markLabel := ""
	if n := len(a.marks); n > 0 {
		markLabel = fmt.Sprintf(" ✓%d", n)
	}
	a.table.SetTitle(tview.Escape(fmt.Sprintf(" %s(%s)[%d]%s%s ", a.res.Title, flabel, len(a.filtered), sortLabel, markLabel)))
	// Re-assert the offset: this clears tview's internal trackEnd flag,
	// which latches during the brief empty draw before data arrives and
	// would otherwise pin the viewport to the bottom of the table.
	if a.pendingSel > 0 && len(a.filtered) > 0 {
		a.table.SetOffset(0, 0)
		a.table.Select(min(a.pendingSel, len(a.filtered)), 0)
		a.pendingSel = 0
	} else if prevRow >= 1 && prevRow <= len(a.filtered) {
		or, oc := a.table.GetOffset()
		a.table.SetOffset(or, oc)
		a.table.Select(prevRow, 0)
	} else if len(a.filtered) > 0 {
		a.table.SetOffset(0, 0)
		a.table.Select(1, 0)
	}
	a.updateInfo()
}

func expansion(col string) int {
	switch col {
	case "NAME", "TITLE", "MESSAGE":
		return 3
	case "TAGS":
		return 2
	default:
		return 0
	}
}

func rowColor(resKey string, r data.Row) tcell.Color {
	key := ""
	if len(r.Cells) > 0 {
		key = strings.ToLower(r.Cells[0])
	}
	switch resKey {
	case "overview":
		status := ""
		if len(r.Cells) > 1 {
			status = strings.ToLower(r.Cells[1])
		}
		switch {
		case strings.Contains(status, "sev-1"), strings.Contains(status, "active"), strings.Contains(status, "alert"):
			return tcell.ColorRed
		case strings.Contains(status, "warn"), strings.Contains(status, "stable"), strings.Contains(status, "sev-2"):
			return tcell.ColorYellow
		case strings.Contains(status, "no data"):
			return tcell.ColorGray
		}
		return tcell.ColorLightGray
	case "monitors":
		switch key {
		case "alert":
			return tcell.ColorRed
		case "warn":
			return tcell.ColorYellow
		case "no data":
			return tcell.ColorGray
		case "ok":
			return tcell.ColorLightGreen
		}
	case "logs":
		switch strings.ToLower(r.Cells[1]) {
		case "error", "critical", "emergency":
			return tcell.ColorRed
		case "warn", "warning":
			return tcell.ColorYellow
		}
		return tcell.ColorLightGray
	case "incidents":
		switch strings.ToLower(r.Cells[2]) {
		case "active":
			return tcell.ColorRed
		case "stable":
			return tcell.ColorYellow
		case "resolved":
			return tcell.ColorLightGreen
		}
	case "contexts":
		if r.Cells[0] == "*" {
			return tcell.ColorLightGreen
		}
	case "events":
		switch strings.ToLower(r.Cells[1]) { // TYPE column
		case "error":
			return tcell.ColorRed
		case "warning", "warn":
			return tcell.ColorYellow
		case "success":
			return tcell.ColorLightGreen
		case "deploy":
			return tcell.ColorOrange
		}
	case "downtimes":
		switch strings.ToLower(r.Cells[0]) { // STATUS column
		case "active":
			return tcell.ColorYellow // something is currently muted — worth noticing
		case "scheduled":
			return tcell.ColorLightSkyBlue
		case "canceled", "ended":
			return tcell.ColorGray
		}
	}
	return tcell.ColorLightSkyBlue
}

func (a *App) updateInfo() {
	age := "-"
	if !a.fetchedAt.IsZero() {
		age = fmt.Sprintf("%ds (ttl %s)", int(time.Since(a.fetchedAt).Seconds()), a.res.TTL)
	}
	if a.loading {
		age = "loading…"
	}
	budget := formatBudget(a.provider.Budget())
	if entries := a.activeEntries(); len(entries) > 1 {
		// Several orgs active: one budget line per org, prefixed by context.
		var lines []string
		for _, e := range entries {
			if b := formatBudget(e.p.Budget()); b != "-" {
				lines = append(lines, tview.Escape(e.name)+": "+b)
			}
		}
		if len(lines) > 0 {
			budget = strings.Join(lines, "\n")
		}
	}
	// Escape: context names like "staging" would otherwise parse as tview
	// color tags inside this dynamic-colors TextView and vanish.
	cur := a.current
	if cur == "" {
		cur = "no context"
	}
	mode := tview.Escape(fmt.Sprintf("%s [%s]", a.provider.Mode(), cur))
	modeColor := "green"
	if a.provider.Mode() == "demo" {
		modeColor = "yellow"
	}
	view := a.res.Title
	switch a.page {
	case "detail":
		view = fmt.Sprintf("%s ▸ %s", a.res.Title, a.detailRow.ID)
	case "help":
		view = "Help"
	}
	if a.watch {
		view += "  [red::b]● WATCH[-:-:-]"
	}
	a.infoTV.SetText(a.theme.recolor(fmt.Sprintf(
		" [orange]Mode:[%s]   %s\n [orange]Site:[white]   %s\n [orange]View:[white]   %s\n [orange]Age:[white]    %s\n [orange]Budget:[white] %s",
		modeColor, mode, a.provider.Site(), view, age, budget)))
}

// formatBudget turns raw X-RateLimit strings into a glanceable, colour-coded
// summary: green >50% headroom, yellow >20%, red at/under 20% — so you see a
// limit coming before it throttles you. Names are shortened for width.
func formatBudget(raw []string) string {
	if len(raw) == 0 {
		return "[gray]n/a (no API calls yet)"
	}
	parts := make([]string, 0, len(raw))
	for _, r := range raw {
		m := budgetLine.FindStringSubmatch(r)
		if m == nil {
			parts = append(parts, "[gray]"+tview.Escape(r))
			continue
		}
		name, rem, lim := m[1], parseIntSafe(m[2]), parseIntSafe(m[3])
		color := "green"
		if lim > 0 {
			switch ratio := float64(rem) / float64(lim); {
			case ratio <= 0.2:
				color = "red"
			case ratio <= 0.5:
				color = "yellow"
			}
		}
		parts = append(parts, fmt.Sprintf("[white]%s [%s]%d/%d[white]", shortBudgetName(name), color, rem, lim))
	}
	return strings.Join(parts, "  ")
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// shortBudgetName trims Datadog's verbose rate-limit names to something that
// fits the header (e.g. "logs_public_search_api" → "logs_search").
func shortBudgetName(n string) string {
	n = strings.TrimSuffix(n, "_api")
	n = strings.ReplaceAll(n, "_public", "")
	return n
}

// visibleLen is the on-screen width of a string, ignoring tview color tags.
func visibleLen(s string) int { return len([]rune(tagRe.ReplaceAllString(s, ""))) }

// padVisible right-pads s with spaces to visible width w (no truncation —
// widgetLines already clipped content to the cell width).
func padVisible(s string, w int) string {
	if n := w - visibleLen(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// clip truncates plain text to max visible runes (max<=0 = no limit).
func clip(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
