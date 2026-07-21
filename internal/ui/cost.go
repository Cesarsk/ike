package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// showCost opens the :cost panel for the current context's org and fetches
// the selected range (a.costMonths, a.costSubOrg). Read-only, at most three
// bounded API calls. ctrl-r re-fetches; 1/3/6/y change the range; s toggles
// the sub-org breakdown; [ and ] pick the month; / filters lines client-side.
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
	a.cost.SetTitle(" Cost ")
	a.cost.SetText(a.theme.recolor("\n  [gray]fetching cost…")).ScrollToBeginning()
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
				a.cost.SetText(a.theme.recolor(renderCostError(cur, err)))
				a.cost.SetTitle(" Cost ")
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

// renderCostPage redraws the panel from the loaded view plus the local
// selection/filter state.
func (a *App) renderCostPage() {
	v := a.costView
	if v == nil {
		return
	}
	if a.costSel >= len(v.Months) {
		a.costSel = 0
	}
	a.cost.SetText(a.theme.recolor(renderCost(v, a.costSel, a.costFilter, a.costSubOrg))).ScrollToBeginning()
	sel := "—"
	if len(v.Months) > 0 {
		sel = v.Months[a.costSel].Month
	}
	a.cost.SetTitle(fmt.Sprintf(" Cost · %s · %s ", v.OrgName, sel))
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

// renderCost draws the spend panel: header totals for the selected month, a
// month-trend section when history is loaded, and the selected month's
// per-product (or per-org) breakdown with proportional bars.
func renderCost(v *data.CostView, sel int, filter string, subOrgs bool) string {
	var b strings.Builder
	scope := "summary"
	if subOrgs {
		scope = "sub-orgs"
	}
	fmt.Fprintf(&b, " [orange::b]Datadog spend[-:-:-] [gray]%s · %s · read-only, updates daily[-]\n\n",
		tview.Escape(v.OrgName), scope)
	if len(v.Months) == 0 {
		b.WriteString("  [gray]no billing data for this range[-]\n")
		return b.String()
	}
	m := v.Months[sel]
	label := "month total"
	if m.Current {
		label = "estimated so far"
	}
	fmt.Fprintf(&b, "  [aqua]%-16s[-]  %s   [gray](%s)[-]\n", label, money(v.Currency, m.Total), m.Month)
	if m.Current && m.Projected > 0 {
		fmt.Fprintf(&b, "  [aqua]%-16s[-]  %s\n", "projected month", money(v.Currency, m.Projected))
	}
	b.WriteString("\n")

	if len(v.Months) > 1 {
		renderCostTrend(&b, v, sel)
	}
	renderCostLines(&b, v.Currency, m, filter, subOrgs)

	b.WriteString("\n [gray]<1/3/6/y> range · <[/]> month · </> filter · <s> sub-orgs · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}

// renderCostTrend draws one row per loaded month — total, bar, and the change
// vs the month before it — marking the selected month.
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
		}
		fmt.Fprintf(b, " %s%s%-9s[-]  %12s  %s%s\n",
			mark, color, m.Month, money(v.Currency, m.Total), costBar(m.Total, maxTotal), suffix)
	}
	b.WriteString("\n")
}

// renderCostLines draws the selected month's breakdown table, applying the
// client-side filter over product (and org) names.
func renderCostLines(b *strings.Builder, currency string, m data.CostMonth, filter string, subOrgs bool) {
	lines := m.Lines
	if filter != "" {
		f := strings.ToLower(filter)
		kept := make([]data.CostLine, 0, len(lines))
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l.Product), f) || strings.Contains(strings.ToLower(l.Org), f) {
				kept = append(kept, l)
			}
		}
		lines = kept
		fmt.Fprintf(b, "  [gray]filter:[-] [aqua]%s[-]  [gray](%d match", tview.Escape(filter), len(lines))
		if len(lines) != 1 {
			b.WriteString("es")
		}
		b.WriteString(")[-]\n\n")
	}
	if len(lines) == 0 {
		if filter != "" {
			b.WriteString("  [gray]no lines match the filter — <esc> or an empty / clears it[-]\n")
		} else {
			b.WriteString("  [gray]no billable usage this month[-]\n")
		}
		return
	}

	maxCost, prodW, orgW := 0.0, len("PRODUCT"), len("ORG")
	for _, l := range lines {
		if l.Total > maxCost {
			maxCost = l.Total
		}
		if n := len(l.Product); n > prodW {
			prodW = n
		}
		if n := len(l.Org); n > orgW {
			orgW = n
		}
	}
	amount := "TOTAL"
	if m.Current {
		amount = "ESTIMATED"
	}
	if subOrgs {
		fmt.Fprintf(b, "  [gray]%-*s  %-*s  %12s  %12s[-]\n", orgW, "ORG", prodW, "PRODUCT", amount, "PROJECTED")
	} else {
		fmt.Fprintf(b, "  [gray]%-*s  %12s  %12s[-]\n", prodW, "PRODUCT", amount, "PROJECTED")
	}
	for _, l := range lines {
		proj := ""
		if l.Projected > 0 {
			proj = money(currency, l.Projected)
		}
		if subOrgs {
			fmt.Fprintf(b, "  %-*s  %-*s  %12s  %12s  %s\n",
				orgW, tview.Escape(l.Org), prodW, tview.Escape(l.Product),
				money(currency, l.Total), proj, costBar(l.Total, maxCost))
		} else {
			fmt.Fprintf(b, "  %-*s  %12s  %12s  %s\n",
				prodW, tview.Escape(l.Product), money(currency, l.Total), proj, costBar(l.Total, maxCost))
		}
	}
}

// costBar is a proportional bar for one line's cost, up to 24 cells.
func costBar(v, max float64) string {
	if max <= 0 {
		return ""
	}
	n := int(v / max * 24)
	if n < 1 && v > 0 {
		n = 1
	}
	return "[aqua]" + strings.Repeat("█", n) + "[-]"
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
