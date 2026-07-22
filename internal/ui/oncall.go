package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// showTeamOnCall opens the on-call panel for a team row (enter on :oncall):
// who is on call right now plus the escalation ladder, from one bounded
// fetch. Read-only. o opens the team's On-Call page, ctrl-r re-fetches.
func (a *App) showTeamOnCall(row data.Row) {
	a.pushNav()
	a.onCallTeam = row
	cur := a.current
	a.onCall.SetTitle(" On-Call ")
	a.onCall.SetText(a.theme.recolor("\n  [gray]fetching on-call…")).ScrollToBeginning()
	a.showPage("oncall")
	prov := a.providerFor(row) // route to the row's origin org
	go func() {
		det, err := prov.TeamOnCall(context.Background(), row.ID)
		a.QueueUpdateDraw(func() {
			if a.page != "oncall" || a.current != cur || a.onCallTeam.ID != row.ID {
				return // navigated away
			}
			team := row.Cells[0]
			if err != nil {
				a.onCall.SetText(a.theme.recolor(renderOnCallError(team, err)))
				return
			}
			a.onCall.SetText(a.theme.recolor(renderOnCall(team, det)))
			a.onCall.SetTitle(fmt.Sprintf(" On-Call · %s ", team))
		})
	}()
}

// openOnCallURL opens the team's On-Call page in the Datadog web UI.
func (a *App) openOnCallURL() {
	if a.onCallTeam.URL != "" {
		a.openURL(a.onCallTeam.URL)
	}
}

// renderOnCallError explains a failed on-call fetch. On-Call is an add-on, so
// a permission or not-found error usually means it is not enabled for this
// org or this team.
func renderOnCallError(team string, err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "403") || strings.Contains(low, "forbidden") ||
		strings.Contains(low, "404") || strings.Contains(low, "not found") ||
		strings.Contains(low, "not authoriz") || strings.Contains(low, "permission") {
		return fmt.Sprintf("\n  [orange]On-Call isn't available here[-]\n\n"+
			"  Datadog On-Call is an add-on product. Either it isn't enabled for this\n"+
			"  org, %q has no on-call set up, or your login can't read it.\n\n"+
			"  [gray]%s[-]", team, tview.Escape(msg))
	}
	return "\n  [red]✗ " + tview.Escape(msg)
}

// renderOnCall draws who is on call now and the escalation ladder.
func renderOnCall(team string, d *data.OnCallDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]On-Call[-:-:-] [gray]%s · read-only[-]\n\n", tview.Escape(team))

	if len(d.OnCall) == 0 && len(d.Escalation) == 0 {
		b.WriteString("  [gray]no on-call configured for this team[-]\n\n" +
			"  [gray]set up a schedule and escalation policy in Datadog On-Call,\n" +
			"  then it shows up here.[-]\n")
		b.WriteString("\n [gray]<o> open in Datadog · <esc> back[-]\n")
		return b.String()
	}

	b.WriteString("  [aqua]on call now[-]\n")
	if len(d.OnCall) == 0 {
		b.WriteString("    [gray]nobody currently on call[-]\n")
	}
	for _, r := range d.OnCall {
		fmt.Fprintf(&b, "    [white]%s[-]  %s\n", tview.Escape(r.Name), onCallHandle(r))
	}

	if len(d.Escalation) > 0 {
		b.WriteString("\n  [aqua]escalation[-] [gray](who gets paged if the level above doesn't answer)[-]\n")
		for _, lvl := range d.Escalation {
			names := make([]string, 0, len(lvl.Responders))
			for _, r := range lvl.Responders {
				names = append(names, tview.Escape(r.Name))
			}
			fmt.Fprintf(&b, "    [gray]%d.[-] %s\n", lvl.Level, strings.Join(names, ", "))
		}
	}

	b.WriteString("\n [gray]<o> open in Datadog · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}

// onCallHandle formats a responder's contact detail (handle, then email) for
// the on-call-now line, dimmed.
func onCallHandle(r data.OnCallResponder) string {
	switch {
	case r.Handle != "":
		return "[gray]@" + tview.Escape(r.Handle) + "[-]"
	case r.Email != "":
		return "[gray]" + tview.Escape(r.Email) + "[-]"
	}
	return ""
}
