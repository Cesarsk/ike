package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// showNotebook opens the reading panel for a notebook row (enter on
// :notebooks): its cells rendered to text, from one bounded fetch. Read-only.
// o opens it in Datadog, ctrl-r re-fetches, esc goes back.
func (a *App) showNotebook(row data.Row) {
	a.pushNav()
	a.notebookRow = row
	cur := a.current
	a.notebook.SetTitle(" Notebook ")
	a.notebook.SetText(a.theme.recolor("\n  [gray]fetching notebook…")).ScrollToBeginning()
	a.showPage("notebook")
	prov := a.providerFor(row)
	go func() {
		v, err := prov.Notebook(context.Background(), row.ID)
		a.QueueUpdateDraw(func() {
			if a.page != "notebook" || a.current != cur || a.notebookRow.ID != row.ID {
				return // navigated away
			}
			if err != nil {
				a.notebook.SetText(a.theme.recolor("\n  [red]✗ " + tview.Escape(err.Error())))
				return
			}
			a.notebook.SetText(a.theme.recolor(renderNotebook(v)))
			a.notebook.SetTitle(fmt.Sprintf(" Notebook · %s ", v.Name))
		})
	}()
}

// openNotebookURL opens the notebook in the Datadog web UI.
func (a *App) openNotebookURL() {
	if a.notebookRow.URL != "" {
		a.openURL(a.notebookRow.URL)
	}
}

// renderNotebook draws a notebook's header and body. The body is plain text
// (markdown verbatim); '#' heading lines are lightly emphasised.
func renderNotebook(v *data.NotebookView) string {
	var b strings.Builder
	meta := v.Author
	if v.Status != "" {
		meta = fmt.Sprintf("%s · %s", v.Author, v.Status)
	}
	fmt.Fprintf(&b, " [orange::b]%s[-:-:-] [gray]%s · read-only[-]\n\n", tview.Escape(v.Name), tview.Escape(meta))
	if strings.TrimSpace(v.Body) == "" {
		b.WriteString("  [gray]this notebook has no readable text cells[-]\n")
	}
	for _, line := range strings.Split(v.Body, "\n") {
		esc := tview.Escape(line)
		if t := strings.TrimLeft(line, "#"); t != line {
			esc = "[aqua::b]" + tview.Escape(strings.TrimSpace(t)) + "[-:-:-]"
		}
		b.WriteString("  " + esc + "\n")
	}
	b.WriteString("\n [gray]<o> open in Datadog · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}
