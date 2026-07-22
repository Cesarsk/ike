package ui

import (
	"strings"

	"github.com/Cesarsk/ike/internal/data"
)

// menuResource is the :menu pseudo-resource: a self-documenting index of every
// command, served from the app's own catalog rather than a Provider. enter on
// a row runs that command, so it doubles as a command palette.
var menuResource = data.Resource{
	Key:     "menu",
	Title:   "Menu",
	Aliases: []string{"menu", "commands", "cmds", "aliases"},
	Columns: []string{"COMMAND", "ALIASES", "OPENS"},
}

// menuCommand is one row of the :menu catalog. Run is the canonical command
// enter executes; it is empty for entries that are keys, not commands.
type menuCommand struct {
	name    string
	aliases string
	opens   string
	run     string
}

// pseudoCommands are the commands that are not provider-backed resources.
// Kept next to execCommand's routing so the two stay in step.
var pseudoCommands = []menuCommand{
	{name: ":overview", aliases: "ov", opens: "Cross-org triage: firing monitors + open incidents", run: "overview"},
	{name: ":cost", aliases: "costs, billing", opens: "This org's Datadog spend: trend, anomalies, drill-down", run: "cost"},
	{name: ":ctx", aliases: "context, contexts", opens: "Your orgs: switch, span, add, edit, delete", run: "ctx"},
	{name: ":menu", aliases: "commands, cmds, aliases", opens: "This command list", run: "menu"},
	{name: ":watch", aliases: "w", opens: "Hands-off refresh mode for the current view (wall display)", run: "watch"},
	{name: ":settings", aliases: "set, config", opens: "Theme and per-view cache TTLs", run: "settings"},
	{name: ":manual", aliases: "instructions, intro", opens: "The getting-started walkthrough", run: "manual"},
	{name: ":help", aliases: "?", opens: "Full keybinding reference", run: "help"},
	{name: ":quit", aliases: "q, exit", opens: "Leave ike", run: "quit"},
}

// showMenu opens the :menu command palette through the standard table
// pipeline, so it filters (/), sorts and selects like any other view.
func (a *App) showMenu() {
	if a.page == "table" && a.res.Key == menuResource.Key {
		return
	}
	if a.res.Key != "" {
		a.pushNav()
	}
	a.res = menuResource
	a.resetView()
	a.rows = nil
	a.filtered = nil
	a.pendingSel = 1
	a.showPage("table")
	a.render()
	a.load(false)
}

// menuRows builds the command catalog: every navigable resource followed by
// the pseudo-commands. Generated from the resource registry, so it can never
// drift out of sync with what actually exists.
func (a *App) menuRows() []data.Row {
	rows := make([]data.Row, 0, len(data.Resources())+len(pseudoCommands))
	for _, r := range data.Resources() {
		rows = append(rows, data.Row{
			ID:    r.Key,
			Cells: []string{":" + r.Key, otherAliases(r), r.Title + resourceHint(r.Key)},
		})
	}
	for _, c := range pseudoCommands {
		rows = append(rows, data.Row{
			ID:    c.run,
			Cells: []string{c.name, c.aliases, c.opens},
		})
	}
	return rows
}

// otherAliases lists a resource's aliases minus its key (the key is already
// the COMMAND cell), comma-separated.
func otherAliases(r data.Resource) string {
	var out []string
	for _, a := range r.Aliases {
		if a != r.Key {
			out = append(out, a)
		}
	}
	return strings.Join(out, ", ")
}

// resourceHint adds a short "what you can do here" note for views whose value
// isn't obvious from the title alone.
func resourceHint(key string) string {
	switch key {
	case "teams":
		return " — enter for members and roles"
	case "oncall":
		return " — enter for who's on call, escalation, paging"
	case "logs":
		return " — / is a Datadog query; l/t/x correlate and drill"
	case "traces":
		return " — t opens the waterfall"
	case "services":
		return " — enter for a service's traces"
	}
	return ""
}
