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
	if a.onCallTeam.ID != row.ID || a.onCallTeam.Ctx != row.Ctx {
		// Switching teams drops any page in flight — it belongs to the old
		// team. Reopening the SAME team keeps it, so a page raised earlier
		// (here or via P on a monitor) can still be acked/resolved.
		a.onCallPageID = ""
	}
	a.onCallTeam = row
	a.onCallDetail = nil
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
			if err != nil {
				a.onCall.SetText(a.theme.recolor(renderOnCallError(row.Cells[0], err)))
				return
			}
			a.onCallDetail = det
			a.renderOnCallPanel()
		})
	}()
}

// renderOnCallPanel redraws the on-call panel from the stored detail plus the
// current paging state, so paging actions can refresh without re-fetching.
func (a *App) renderOnCallPanel() {
	if a.onCallDetail == nil {
		return
	}
	team := a.onCallTeam.Cells[0]
	a.onCall.SetText(a.theme.recolor(renderOnCall(team, a.onCallDetail, a.onCallPageID))).ScrollToBeginning()
	a.onCall.SetTitle(fmt.Sprintf(" On-Call · %s ", team))
}

// openOnCallURL opens the team's On-Call page in the Datadog web UI.
func (a *App) openOnCallURL() {
	if a.onCallTeam.URL != "" {
		a.openURL(a.onCallTeam.URL)
	}
}

// startPageTeam prompts for a page title; promptDone runs the confirm + page.
func (a *App) startPageTeam() {
	if a.onCallDetail == nil {
		return
	}
	a.openPrompt(promptPageTitle)
}

// confirmPageTeam is called from promptDone with the typed title: it confirms,
// then raises a high-urgency page against the team. Paging wakes a human, so
// it is always behind the confirm and faked in demo mode.
func (a *App) confirmPageTeam(title string) {
	if title == "" {
		return
	}
	team := a.onCallTeam
	a.showConfirm(
		fmt.Sprintf("Page team %q, high urgency?\n\n  %q\n\nThis alerts whoever is on call right now.", team.Cells[0], title),
		[]string{"Cancel", "Page"},
		func(label string) {
			if label != "Page" {
				return
			}
			prov := a.providerFor(team)
			go func() {
				id, err := prov.PageTeam(context.Background(), team.ID, title, "high", "")
				a.QueueUpdateDraw(func() {
					if err != nil {
						a.flash("✗ page: "+err.Error(), true)
						return
					}
					a.onCallPageID = id
					a.flash("paged "+team.Cells[0], false)
					if a.page == "oncall" {
						a.renderOnCallPanel()
					}
				})
			}()
		})
}

// pageAction runs an acknowledge/escalate/resolve on the page raised from this
// panel, behind a confirm. A successful resolve clears the active page.
func (a *App) pageAction(action string) {
	if a.onCallPageID == "" {
		return
	}
	id := a.onCallPageID
	team := a.providerFor(a.onCallTeam)
	label := strings.ToUpper(action[:1]) + action[1:]
	a.showConfirm(fmt.Sprintf("%s page %s?", label, id),
		[]string{"Cancel", label},
		func(label string) {
			if !strings.EqualFold(label, action) {
				return
			}
			go func() {
				var err error
				switch action {
				case "acknowledge":
					err = team.AckPage(context.Background(), id)
				case "escalate":
					err = team.EscalatePage(context.Background(), id)
				case "resolve":
					err = team.ResolvePage(context.Background(), id)
				}
				a.QueueUpdateDraw(func() {
					if err != nil {
						a.flash("✗ "+action+": "+err.Error(), true)
						return
					}
					a.flash(action+"d page", false)
					if action == "resolve" {
						a.onCallPageID = "" // page closed
					}
					if a.page == "oncall" {
						a.renderOnCallPanel()
					}
				})
			}()
		})
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

// renderOnCall draws who is on call now, the escalation ladder (with per-step
// delays where known), and the paging controls. pageID is set once a page has
// been raised from this panel.
func renderOnCall(team string, d *data.OnCallDetail, pageID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]On-Call[-:-:-] [gray]%s · read-only except paging[-]\n\n", tview.Escape(team))

	if len(d.OnCall) == 0 && len(d.Escalation) == 0 {
		b.WriteString("  [gray]no on-call configured for this team[-]\n\n" +
			"  [gray]set up a schedule and escalation policy in Datadog On-Call,\n" +
			"  then it shows up here.[-]\n")
		renderPaging(&b, pageID)
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
			delay := ""
			if lvl.DelayMin > 0 {
				delay = fmt.Sprintf(" [gray](after %dm)[-]", lvl.DelayMin)
			}
			fmt.Fprintf(&b, "    [gray]%d.[-] %s%s\n", lvl.Level, strings.Join(names, ", "), delay)
		}
	}

	renderPaging(&b, pageID)
	b.WriteString("\n [gray]<o> open in Datadog · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}

// renderPaging draws the paging controls: a page prompt, or the lifecycle
// actions once a page has been raised from this panel.
func renderPaging(b *strings.Builder, pageID string) {
	b.WriteString("\n  [aqua]paging[-]\n")
	if pageID == "" {
		b.WriteString("    [aqua]<p>[-] page this team [gray](confirm-gated; alerts whoever is on call)[-]\n")
		return
	}
	fmt.Fprintf(b, "    active page [white]%s[-]\n", tview.Escape(pageID))
	b.WriteString("    [aqua]<a>[-] acknowledge   [aqua]<e>[-] escalate   [aqua]<r>[-] resolve\n")
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
