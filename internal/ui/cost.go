package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// showCost opens the :cost panel for the current context's org: this month's
// estimated spend so far and the projected total, by product. One bounded
// fetch (two API calls), read-only. ctrl-r re-fetches.
func (a *App) showCost() {
	a.pushNav()
	cur := a.current
	a.cost.SetTitle(" Cost ")
	a.cost.SetText(a.theme.recolor("\n  [gray]fetching cost…")).ScrollToBeginning()
	a.showPage("cost")
	prov := a.provider // the current org's provider
	go func() {
		v, err := prov.Cost(context.Background())
		a.QueueUpdateDraw(func() {
			if a.page != "cost" || a.current != cur {
				return // navigated away
			}
			if err != nil {
				a.cost.SetText(a.theme.recolor(renderCostError(cur, err)))
				a.cost.SetTitle(" Cost ")
				return
			}
			a.cost.SetText(a.theme.recolor(renderCost(v)))
			a.cost.SetTitle(fmt.Sprintf(" Cost · %s · %s ", v.OrgName, v.Month))
		})
	}()
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

// renderCost draws the spend breakdown: header totals, then a per-product
// table with a proportional bar sized to the largest estimated line.
func renderCost(v *data.CostView) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]Datadog spend[-:-:-] [gray]%s · %s · read-only, updates daily[-]\n\n",
		tview.Escape(v.OrgName), v.Month)
	fmt.Fprintf(&b, "  [aqua]estimated so far[-]  %s\n", money(v.Currency, v.Estimated))
	if v.Projected > 0 {
		fmt.Fprintf(&b, "  [aqua]projected month[-]   %s\n", money(v.Currency, v.Projected))
	}
	b.WriteString("\n")
	if len(v.Lines) == 0 {
		b.WriteString("  [gray]no billable usage this month[-]\n")
		return b.String()
	}

	maxEst := 0.0
	prodW := len("PRODUCT")
	for _, l := range v.Lines {
		if l.Estimated > maxEst {
			maxEst = l.Estimated
		}
		if n := len(l.Product); n > prodW {
			prodW = n
		}
	}
	fmt.Fprintf(&b, "  [gray]%-*s  %12s  %12s[-]\n", prodW, "PRODUCT", "ESTIMATED", "PROJECTED")
	for _, l := range v.Lines {
		proj := ""
		if l.Projected > 0 {
			proj = money(v.Currency, l.Projected)
		}
		fmt.Fprintf(&b, "  %-*s  %12s  %12s  %s\n",
			prodW, tview.Escape(l.Product), money(v.Currency, l.Estimated), proj, costBar(l.Estimated, maxEst))
	}
	b.WriteString("\n [gray]<ctrl-r> refresh · <esc> back · switch org (:ctx) for another org's spend[-]\n")
	return b.String()
}

// costBar is a proportional bar for one line's estimated cost, up to 24 cells.
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
