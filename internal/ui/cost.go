package ui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// A month-over-month move is flagged as an anomaly only when it is both large
// in relative terms and large in absolute terms — a 3x jump on a $5 line is
// noise, not an anomaly.
const (
	costAnomalyPct = 30.0
	costAnomalyAbs = 100.0
)

// costRender bundles the app-side state the pure cost renderers need.
type costRender struct {
	view     *data.CostView
	sel      int    // selected month index
	filter   string // client-side substring filter over org/product
	orgFocus string // sub-org focus ("" = all; f cycles)
	subOrgs  bool
}

// costLineDelta pairs a breakdown line with its change vs the same line one
// month earlier. For the current month the projection is compared (a partial
// month against a full one would always look like a drop).
type costLineDelta struct {
	line      data.CostLine
	pct       float64
	hasPrev   bool
	isNew     bool // product had no line the month before
	anomalous bool
}

// showCost opens the :cost panel for the current context's org and fetches
// the selected range (a.costMonths, a.costSubOrg). Read-only, at most three
// bounded API calls. ctrl-r re-fetches; 1/3/6/y change the range; s toggles
// the sub-org breakdown; f cycles the sub-org focus; [ and ] pick the month;
// / filters lines client-side; enter drills into the selected line's
// month-by-month history; o opens the billing page.
func (a *App) showCost() {
	if a.page != "cost" {
		a.pushNav()
	}
	if a.costMonths < 1 {
		a.costMonths = 1
	}
	cur := a.current
	a.costView = nil
	a.costSel = 0
	a.costOrgFocus = ""
	a.costRows = nil
	a.costTbl.Clear()
	a.costFlex.SetTitle(" Cost ")
	a.costHead.SetText(a.theme.recolor("\n  [gray]fetching cost…"))
	a.showPage("cost")
	prov := a.provider // the current org's provider
	opts := data.CostOptions{Months: a.costMonths, SubOrgs: a.costSubOrg}
	go func() {
		v, err := prov.Cost(context.Background(), opts)
		a.QueueUpdateDraw(func() {
			if a.page != "cost" || a.current != cur {
				return // navigated away
			}
			if err != nil {
				a.costHead.SetText(a.theme.recolor(renderCostError(cur, err)))
				a.costFlex.SetTitle(" Cost ")
				return
			}
			a.costView = v
			a.costSel = 0
			a.renderCostPage()
		})
	}()
}

// setCostRange switches the month range and re-fetches.
func (a *App) setCostRange(months int) {
	if a.costMonths == months && a.costView != nil {
		return
	}
	a.costMonths = months
	a.showCost()
}

// moveCostMonth shifts the selected month by delta (]=older, [=newer) and
// re-renders locally — the data is already loaded.
func (a *App) moveCostMonth(delta int) {
	if a.costView == nil {
		return
	}
	sel := a.costSel + delta
	if sel < 0 || sel >= len(a.costView.Months) {
		return
	}
	a.costSel = sel
	a.renderCostPage()
}

// cycleCostOrg steps the sub-org focus through all → each org → all, so one
// child org's breakdown can be looked at in isolation.
func (a *App) cycleCostOrg() {
	if a.costView == nil || !a.costSubOrg || len(a.costView.Months) == 0 {
		return
	}
	orgs := costOrgs(a.costView.Months[a.costSel])
	if len(orgs) == 0 {
		return
	}
	next := ""
	if a.costOrgFocus == "" {
		next = orgs[0]
	} else {
		for i, o := range orgs {
			if o == a.costOrgFocus && i+1 < len(orgs) {
				next = orgs[i+1]
				break
			}
		}
	}
	a.costOrgFocus = next
	a.renderCostPage()
}

// openCostURL opens the org's billing/usage page in the Datadog web UI.
func (a *App) openCostURL() {
	if a.costView == nil {
		return
	}
	a.openURL(a.costView.URL)
}

// openCostProduct drills into the selected breakdown line: that product's
// month-by-month history across the loaded range, rendered from data already
// on hand — no extra API calls.
func (a *App) openCostProduct() {
	row, _ := a.costTbl.GetSelection()
	if a.costView == nil || row < 1 || row > len(a.costRows) {
		return
	}
	l := a.costRows[row-1].line
	a.pushNav()
	a.costProd.SetText(a.theme.recolor(renderCostProduct(a.costView, l.Org, l.Product))).ScrollToBeginning()
	title := l.Product
	if l.Org != "" {
		title = l.Org + " · " + l.Product
	}
	a.costProd.SetTitle(" Cost · " + title + " ")
	a.showPage("costprod")
}

// renderCostPage redraws the panel from the loaded view plus the local
// selection/filter state: header text on top, the breakdown table below.
func (a *App) renderCostPage() {
	v := a.costView
	if v == nil {
		return
	}
	if a.costSel >= len(v.Months) {
		a.costSel = 0
	}
	r := costRender{view: v, sel: a.costSel, filter: a.costFilter, orgFocus: a.costOrgFocus, subOrgs: a.costSubOrg}
	var deltas []costLineDelta
	if len(v.Months) > 0 {
		m := v.Months[a.costSel]
		var prev *data.CostMonth
		if a.costSel+1 < len(v.Months) {
			prev = &v.Months[a.costSel+1]
		}
		deltas = costDeltas(m, prev, a.costFilter, a.costOrgFocus)
	}
	head := renderCostHead(r, deltas)
	a.costHead.SetText(a.theme.recolor(head))
	a.costFlex.ResizeItem(a.costHead, strings.Count(head, "\n")+1, 0)
	a.fillCostTable(deltas)
	sel := "—"
	if len(v.Months) > 0 {
		sel = v.Months[a.costSel].Month
	}
	a.costFlex.SetTitle(fmt.Sprintf(" Cost · %s · %s ", v.OrgName, sel))
}

// fillCostTable populates the breakdown table for the selected month.
func (a *App) fillCostTable(deltas []costLineDelta) {
	a.costTbl.Clear()
	a.costRows = deltas
	m := data.CostMonth{}
	if a.costView != nil && a.costSel < len(a.costView.Months) {
		m = a.costView.Months[a.costSel]
	}
	amount := "TOTAL"
	if m.Current {
		amount = "ESTIMATED"
	}
	headers := []string{"PRODUCT", amount, "PROJECTED", "Δ PREV", ""}
	if a.costSubOrg {
		headers = append([]string{"ORG"}, headers...)
	}
	for c, h := range headers {
		a.costTbl.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(tcell.ColorWhite).SetAttributes(tcell.AttrBold).SetSelectable(false).
			SetAlign(alignFor(h)))
	}
	if len(deltas) == 0 {
		msg := " no billable usage this month"
		if a.costFilter != "" || a.costOrgFocus != "" {
			msg = " nothing to show — a filter or org focus is active; <esc> clears the filter"
		}
		a.costTbl.SetCell(1, 0, tview.NewTableCell(msg).SetSelectable(false))
		return
	}
	maxCost := 0.0
	for _, d := range deltas {
		if d.line.Total > maxCost {
			maxCost = d.line.Total
		}
	}
	barColor := tcell.GetColor(a.theme.Key)
	for i, d := range deltas {
		l := d.line
		proj := ""
		if l.Projected > 0 {
			proj = money(a.costView.Currency, l.Projected)
		}
		deltaTxt, deltaColor := costDeltaText(d)
		cells := []struct {
			text  string
			color tcell.Color
			align int
		}{
			{tview.Escape(l.Product), tcell.ColorDefault, tview.AlignLeft},
			{money(a.costView.Currency, l.Total), tcell.ColorDefault, tview.AlignRight},
			{proj, tcell.ColorDefault, tview.AlignRight},
			{deltaTxt, deltaColor, tview.AlignRight},
			{costBarPlain(l.Total, maxCost), barColor, tview.AlignLeft},
		}
		if a.costSubOrg {
			cells = append([]struct {
				text  string
				color tcell.Color
				align int
			}{{tview.Escape(l.Org), tcell.ColorDefault, tview.AlignLeft}}, cells...)
		}
		for c, cell := range cells {
			tc := tview.NewTableCell(cell.text).SetAlign(cell.align).SetExpansion(boolToInt(c == len(cells)-1))
			if cell.color != tcell.ColorDefault {
				tc.SetTextColor(cell.color)
			}
			a.costTbl.SetCell(i+1, c, tc)
		}
	}
	a.costTbl.Select(1, 0).ScrollToBeginning()
}

// alignFor right-aligns the numeric column headers.
func alignFor(header string) int {
	switch header {
	case "TOTAL", "ESTIMATED", "PROJECTED", "Δ PREV":
		return tview.AlignRight
	}
	return tview.AlignLeft
}

// renderCostError explains a failed cost fetch. The usage/billing endpoints
// are admin-scoped, so the common case is a permission denial — say so plainly
// instead of dumping a raw 403.
func renderCostError(ctxName string, err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "403") || strings.Contains(low, "forbidden") ||
		strings.Contains(low, "not authoriz") || strings.Contains(low, "permission") {
		return fmt.Sprintf("\n  [orange]Datadog cost is admin-scoped[-]\n\n"+
			"  The usage/billing API needs the [aqua]usage_read[-] permission, which is\n"+
			"  usually limited to org admins. Context %q can't read it.\n\n"+
			"  [gray]%s[-]", ctxName, tview.Escape(msg))
	}
	return "\n  [red]✗ " + tview.Escape(msg)
}

// renderCostHead draws everything above the breakdown table: totals for the
// selected month, the anomaly summary, the sub-org scope (and, when only one
// org is visible, why the breakdown needs the root org), the month trend, and
// the filter status.
func renderCostHead(r costRender, deltas []costLineDelta) string {
	v := r.view
	var b strings.Builder
	scope := "summary"
	if r.subOrgs {
		scope = "sub-orgs"
	}
	fmt.Fprintf(&b, " [orange::b]Datadog spend[-:-:-] [gray]%s · %s · read-only, updates daily[-]\n\n",
		tview.Escape(v.OrgName), scope)
	if len(v.Months) == 0 {
		b.WriteString("  [gray]no billing data for this range[-]")
		return b.String()
	}
	m := v.Months[r.sel]
	label := "month total"
	if m.Current {
		label = "estimated so far"
	}
	fmt.Fprintf(&b, "  [aqua]%-16s[-]  %s   [gray](%s)[-]\n", label, money(v.Currency, m.Total), m.Month)
	if m.Current && m.Projected > 0 {
		fmt.Fprintf(&b, "  [aqua]%-16s[-]  %s\n", "projected month", money(v.Currency, m.Projected))
	}
	if n := countAnomalies(deltas); n > 0 {
		plural := ""
		if n != 1 {
			plural = "s"
		}
		fmt.Fprintf(&b, "  [orange]⚠ %d unusual move%s vs previous month[-]\n", n, plural)
	}
	if r.subOrgs {
		renderCostScope(&b, v.Currency, m, r.orgFocus)
	}
	if len(v.Months) > 1 {
		b.WriteString("\n")
		renderCostTrend(&b, v, r.sel)
	}
	if r.filter != "" {
		fmt.Fprintf(&b, "\n  [gray]filter:[-] [aqua]%s[-]  [gray](%d match", tview.Escape(r.filter), len(deltas))
		if len(deltas) != 1 {
			b.WriteString("es")
		}
		b.WriteString(")[-]\n")
	}
	return b.String()
}

// renderCostScope draws the sub-org focus line, and — when the response has
// no child orgs to show — explains that the breakdown needs the root org.
func renderCostScope(b *strings.Builder, currency string, m data.CostMonth, orgFocus string) {
	if orgFocus == "" {
		b.WriteString("  [gray]sub-orgs:[-] [aqua]all[-] [gray](f focuses one)[-]\n")
	} else {
		var sub float64
		for _, l := range m.Lines {
			if l.Org == orgFocus {
				sub += l.Total
			}
		}
		fmt.Fprintf(b, "  [gray]sub-orgs:[-] [aqua]%s[-] · %s [gray](f cycles)[-]\n",
			tview.Escape(orgFocus), money(currency, sub))
	}
	if len(costOrgs(m)) <= 1 {
		b.WriteString("  [orange]only one org visible[-] — either this org has no sub-orgs, or this\n" +
			"  context is signed into a sub-org. The sub-org breakdown is served from\n" +
			"  the root organization: add a context for it in [aqua]:ctx[-] and switch there.\n")
	}
}

// renderCostTrend draws one row per loaded month — total, bar, and the change
// vs the month before it — marking the selected month and flagging months
// whose total moved anomalously.
func renderCostTrend(b *strings.Builder, v *data.CostView, sel int) {
	maxTotal := 0.0
	for _, m := range v.Months {
		if m.Total > maxTotal {
			maxTotal = m.Total
		}
	}
	fmt.Fprintf(b, "  [gray]%-9s  %12s[-]\n", "MONTH", "TOTAL")
	for i, m := range v.Months {
		mark, color := " ", "[white]"
		if i == sel {
			mark, color = "▶", "[aqua]"
		}
		suffix := ""
		switch {
		case m.Current:
			suffix = " [gray](in progress)[-]"
		case i+1 < len(v.Months) && v.Months[i+1].Total > 0:
			prev := v.Months[i+1].Total
			pct := (m.Total - prev) / prev * 100
			c := "[green]"
			if pct > 0 {
				c = "[red]" // cost going up is the bad direction
			}
			suffix = fmt.Sprintf(" %s%+.0f%%[-]", c, pct)
			if math.Abs(pct) >= costAnomalyPct && math.Abs(m.Total-prev) >= costAnomalyAbs {
				suffix += "[orange]⚠[-]"
			}
		}
		fmt.Fprintf(b, " %s%s%-9s[-]  %12s  %s%s\n",
			mark, color, m.Month, money(v.Currency, m.Total), costBar(m.Total, maxTotal), suffix)
	}
}

// renderCostProduct draws one breakdown line's month-by-month history across
// the loaded range — its total, change, and share of that month's bill.
func renderCostProduct(v *data.CostView, org, product string) string {
	var b strings.Builder
	name := product
	if org != "" {
		name = org + " · " + product
	}
	fmt.Fprintf(&b, " [orange::b]%s[-:-:-] [gray]month by month · from the loaded range, no extra API calls[-]\n\n",
		tview.Escape(name))

	type monthLine struct {
		month      string
		current    bool
		total      float64
		projected  float64
		monthTotal float64
		found      bool
	}
	rows := make([]monthLine, 0, len(v.Months))
	maxTotal := 0.0
	for _, m := range v.Months {
		ml := monthLine{month: m.Month, current: m.Current, monthTotal: m.Total}
		for _, l := range m.Lines {
			if l.Org == org && l.Product == product {
				ml.total, ml.projected, ml.found = l.Total, l.Projected, true
				break
			}
		}
		if ml.total > maxTotal {
			maxTotal = ml.total
		}
		rows = append(rows, ml)
	}

	fmt.Fprintf(&b, "  [gray]%-9s  %12s  %7s  %6s[-]\n", "MONTH", "TOTAL", "Δ PREV", "SHARE")
	for i, ml := range rows {
		total := money(v.Currency, ml.total)
		if !ml.found {
			total = "—"
		}
		delta := ""
		if i+1 < len(rows) && rows[i+1].found && rows[i+1].total > 0 && ml.found {
			cur := ml.total
			if ml.current && ml.projected > 0 {
				cur = ml.projected // compare full-month projection, not a partial accrual
			}
			pct := (cur - rows[i+1].total) / rows[i+1].total * 100
			c := "[green]"
			if pct > 0 {
				c = "[red]"
			}
			delta = c + padLeftCells(fmt.Sprintf("%+.0f%%", pct), 7) + "[-]"
			if math.Abs(pct) >= costAnomalyPct && math.Abs(cur-rows[i+1].total) >= costAnomalyAbs {
				delta += "[orange]⚠[-]"
			}
		} else {
			delta = padLeftCells("", 7)
		}
		share := ""
		if ml.found && ml.monthTotal > 0 {
			share = fmt.Sprintf("%5.1f%%", ml.total/ml.monthTotal*100)
		}
		suffix := ""
		if ml.current {
			suffix = " [gray](in progress)[-]"
			if ml.projected > 0 {
				suffix = fmt.Sprintf(" [gray](in progress · projected %s)[-]", money(v.Currency, ml.projected))
			}
		}
		fmt.Fprintf(&b, "  %-9s  %12s  %s  %6s  %s%s\n",
			ml.month, total, delta, share, costBar(ml.total, maxTotal), suffix)
	}
	if len(rows) == 1 {
		b.WriteString("\n  [gray]only this month is loaded — press 3, 6 or y in the cost view to load\n  history and see this product's trend[-]\n")
	}
	b.WriteString("\n [gray]<esc> back to the breakdown[-]\n")
	return b.String()
}

// costDeltas computes each visible line's change vs the previous month,
// applying the org focus and client-side filter. A move is anomalous when it
// clears both the relative and absolute thresholds; a product with no line
// the month before is "new" (anomalous if it is already material).
func costDeltas(m data.CostMonth, prev *data.CostMonth, filter, orgFocus string) []costLineDelta {
	prevTotals := map[string]float64{}
	if prev != nil {
		for _, l := range prev.Lines {
			prevTotals[l.Org+"\x00"+l.Product] = l.Total
		}
	}
	f := strings.ToLower(filter)
	out := make([]costLineDelta, 0, len(m.Lines))
	for _, l := range m.Lines {
		if orgFocus != "" && l.Org != orgFocus {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(l.Product), f) &&
			!strings.Contains(strings.ToLower(l.Org), f) {
			continue
		}
		d := costLineDelta{line: l}
		cur := l.Total
		if m.Current && l.Projected > 0 {
			cur = l.Projected // compare full-month projection, not a partial accrual
		}
		if prev != nil {
			pt, ok := prevTotals[l.Org+"\x00"+l.Product]
			switch {
			case ok && pt > 0:
				d.hasPrev = true
				d.pct = (cur - pt) / pt * 100
				d.anomalous = math.Abs(d.pct) >= costAnomalyPct && math.Abs(cur-pt) >= costAnomalyAbs
			case !ok:
				d.isNew = true
				d.anomalous = cur >= costAnomalyAbs
			}
		}
		out = append(out, d)
	}
	return out
}

// countAnomalies counts the flagged lines in a delta set.
func countAnomalies(deltas []costLineDelta) int {
	n := 0
	for _, d := range deltas {
		if d.anomalous {
			n++
		}
	}
	return n
}

// costDeltaText renders one line's delta as plain text plus the color that
// carries its meaning (red = up, green = down, orange = new/anomalous).
func costDeltaText(d costLineDelta) (string, tcell.Color) {
	switch {
	case d.isNew:
		txt := "new"
		if d.anomalous {
			txt += "⚠"
		}
		return txt, tcell.ColorOrange
	case d.hasPrev:
		txt := fmt.Sprintf("%+.0f%%", d.pct)
		if d.anomalous {
			txt += "⚠"
		}
		if d.pct > 0 {
			return txt, tcell.ColorRed // cost going up is the bad direction
		}
		return txt, tcell.ColorGreen
	}
	return "", tcell.ColorDefault
}

// costOrgs lists the distinct org names in a month's lines, in line order
// (i.e. highest-cost first). Empty in summary view.
func costOrgs(m data.CostMonth) []string {
	seen := map[string]bool{}
	var orgs []string
	for _, l := range m.Lines {
		if l.Org == "" || seen[l.Org] {
			continue
		}
		seen[l.Org] = true
		orgs = append(orgs, l.Org)
	}
	return orgs
}

// padLeftCells right-aligns s to w screen cells, counting runes — fmt's %*s
// counts bytes, which misaligns multi-byte runes like ⚠.
func padLeftCells(s string, w int) string {
	if n := w - utf8.RuneCountInString(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// costBar is a proportional bar for one line's cost, up to 24 cells, wrapped
// in the key color tag for dynamic text.
func costBar(v, max float64) string {
	bar := costBarPlain(v, max)
	if bar == "" {
		return ""
	}
	return "[aqua]" + bar + "[-]"
}

// costBarPlain is costBar without color tags, for table cells that carry
// their color on the cell itself.
func costBarPlain(v, max float64) string {
	if max <= 0 {
		return ""
	}
	n := int(v / max * 24)
	if n < 1 && v > 0 {
		n = 1
	}
	return strings.Repeat("█", n)
}

// money formats an amount with a thousands-separated whole part and a currency
// symbol ($ for USD, else the code).
func money(currency string, v float64) string {
	sym := "$"
	if currency != "" && currency != "USD" {
		sym = currency + " "
	}
	whole := fmt.Sprintf("%.0f", v)
	neg := strings.HasPrefix(whole, "-")
	whole = strings.TrimPrefix(whole, "-")
	var out []byte
	for i, c := range []byte(whole) {
		if i > 0 && (len(whole)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	s := sym + string(out)
	if neg {
		s = "-" + s
	}
	return s
}
