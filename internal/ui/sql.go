package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// sqlCellMax bounds a rendered cell so one wide value cannot push the table
// off the pane; the zoom-less page shows the clipped marker instead.
const sqlCellMax = 40

// openSQLExplorer shows the :sql page: a DDSQL query rendered as a table,
// with the dashboard time-range keys.
func (a *App) openSQLExplorer() {
	if a.page == "sql" {
		return
	}
	if a.res.Key != "" {
		a.pushNav()
	}
	a.showPage("sql")
	if a.sqlQuery == "" {
		a.sqlTV.SetTitle(" SQL ")
		a.sqlTV.SetText(a.theme.recolor(sqlIntro)).ScrollToBeginning()
		return
	}
	a.loadSQL()
}

const sqlIntro = `
 [orange::b]DDSQL explorer[-:-:-]

 Press [aqua]/[-] and type a DDSQL query. The datasets are your cloud resource
 inventory (aws.*, gcp.*, azure.*, alibabacloud.*), one table per resource type:

   [darkcyan]SELECT instance_id, instance_type, state FROM aws.ec2_instance LIMIT 50[-]
   [darkcyan]SELECT account_id, count(*) AS n FROM aws.ec2_instance GROUP BY account_id ORDER BY n DESC[-]
   [darkcyan]SELECT db_instance_identifier, engine, engine_version FROM aws.rds_db_instance[-]

 Results are capped at 200 rows and say so when cut.

 [gray]<1-6> window 15m..1mo · <w> custom · <ctrl-r> refresh · <esc> back[-]
`

// loadSQL runs the current query over the current window.
func (a *App) loadSQL() {
	query, window, label := a.sqlQuery, a.sqlWindow, a.sqlLabel
	if window <= 0 {
		window, label = time.Hour, "1h"
	}
	a.sqlTV.SetTitle(fmt.Sprintf(" SQL · last %s ", label))
	a.sqlTV.SetText(a.theme.recolor("\n  [gray]running…")).ScrollToBeginning()
	prov := a.provider
	go func() {
		res, err := prov.SQLQuery(context.Background(), query, window)
		a.QueueUpdateDraw(func() {
			if a.page != "sql" {
				return // navigated away
			}
			if err != nil {
				a.sqlTV.SetText(a.theme.recolor("\n  [red]✗ " + tview.Escape(err.Error())))
				return
			}
			_, _, paneW, _ := a.sqlTV.GetInnerRect()
			a.sqlTV.SetText(a.theme.recolor(renderSQLBody(res, label, paneW))).ScrollToBeginning()
		})
	}()
}

// setSQLWindow applies a window and re-runs the query when there is one.
func (a *App) setSQLWindow(label string, d time.Duration) {
	a.sqlWindow, a.sqlLabel = d, label
	if a.sqlQuery != "" {
		a.loadSQL()
	}
}

// renderSQLBody draws the result: query, row count, warnings, then an
// aligned table whose columns are sized to their content (capped).
func renderSQLBody(res *data.SQLResult, label string, paneW int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n [darkcyan]%s[-]  [gray]last %s[-]\n", tview.Escape(res.Query), label)
	count := fmt.Sprintf("%d rows", len(res.Rows))
	if res.Truncated {
		count += " [yellow](capped — add LIMIT or narrow the query)[-]"
	}
	fmt.Fprintf(&b, " [gray]%s[-]\n", count)
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, " [yellow]! %s[-]\n", tview.Escape(w))
	}
	b.WriteString("\n")
	if len(res.Columns) == 0 {
		b.WriteString(" [gray]no columns returned[-]\n")
	} else {
		widths := make([]int, len(res.Columns))
		for i, c := range res.Columns {
			widths[i] = len([]rune(c))
		}
		for _, row := range res.Rows {
			for i := range res.Columns {
				if i < len(row) {
					if n := len([]rune(clip(row[i], sqlCellMax))); n > widths[i] {
						widths[i] = n
					}
				}
			}
		}
		b.WriteString(" [white::b]")
		for i, c := range res.Columns {
			b.WriteString(padRight(tview.Escape(c), widths[i]))
			if i < len(res.Columns)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteString("[-:-:-]\n ")
		for i := range res.Columns {
			b.WriteString(strings.Repeat("─", widths[i]))
			if i < len(res.Columns)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteString("\n")
		for _, row := range res.Rows {
			b.WriteString(" ")
			for i := range res.Columns {
				cell := ""
				if i < len(row) {
					cell = clip(row[i], sqlCellMax)
				}
				b.WriteString(padRight(tview.Escape(cell), widths[i]))
				if i < len(res.Columns)-1 {
					b.WriteString("  ")
				}
			}
			b.WriteString("\n")
		}
	}
	_ = paneW
	b.WriteString("\n [gray]</> query · <1-6> window 15m..1mo · <w> custom · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}

// padRight pads s with spaces to width runes (tview tags excluded by the
// caller passing escaped plain text).
func padRight(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
