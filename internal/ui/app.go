package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/config"
	"github.com/Cesarsk/ike/internal/data"
)

type promptMode int

const (
	promptNone promptMode = iota
	promptCmd
	promptFilter
)

// ContextInfo describes one selectable Datadog org context for the :ctx view.
type ContextInfo struct {
	Name string
	Site string
	Keys string // where the credentials come from, e.g. "$IKE_DEV_API_KEY"
}

// ProviderFactory builds a fresh Provider for a named context.
type ProviderFactory func(name string) (data.Provider, error)

// Options wires the app to its environment. AddContext persists a context
// added via the TUI form (:ctx → a) — credentials go to the OS keychain in
// live mode; DeleteContext removes one (ctrl-d). Either may be nil to
// disable the corresponding action.
type Options struct {
	Contexts []ContextInfo
	Current  string
	Factory  ProviderFactory
	// AddContext persists a TUI-added context. Exactly one of
	// (apiKey, appKey) or token is provided; subdomain may be empty.
	AddContext    func(name, site, apiKey, appKey, token, subdomain string) (ContextInfo, error)
	DeleteContext func(name string) error
	// ConfigPath is the contexts file, opened by 'e' in :ctx via $EDITOR.
	// Empty disables in-app editing (demo mode).
	ConfigPath string
	// ReloadContexts re-reads the config after an edit and returns the
	// fresh context list; an error keeps the previous in-memory state.
	ReloadContexts func() ([]ContextInfo, error)
	Refresh        time.Duration
}

// ctxResource is the :ctx pseudo-resource. It is rendered like any table but
// served from the app's own context list, never from a Provider, and enter
// switches context instead of opening a detail view.
var ctxResource = data.Resource{
	Key:     "contexts",
	Title:   "Contexts",
	Columns: []string{"ACTIVE", "NAME", "SITE", "KEYS"},
}

// App is the k9s-style shell: header (info + hints), one resource table,
// a command/filter prompt and a status line.
type App struct {
	*tview.Application
	provider     *data.Cached
	opts         Options
	ctxInfos     []ContextInfo
	current      string // active context name
	refreshEvery time.Duration

	content *tview.Pages // "table" | "detail" | "help" | "ctxform" (+ "confirm" overlay)
	infoTV  *tview.TextView
	hintTV  *tview.TextView
	table   *tview.Table
	prompt  *tview.InputField
	status  *tview.TextView
	footer  *tview.Pages
	detail  *tview.TextView
	ctxForm *tview.Form
	formErr *tview.TextView
	confirm *tview.Modal

	res         data.Resource
	rows        []data.Row
	filtered    []int
	filter      string            // '/' text filter: substring across all cells
	stateFilter string            // 0-4 quick filter: exact match on the STATE column
	queries     map[string]string // per-resource server-side query (logs)
	fetchedAt   time.Time
	loading     bool
	promptM     promptMode
	page        string // current content page: "table", "help", "detail"
	detailRow   data.Row
	stack       []navEntry // navigation history, k9s-style: esc pops
	pendingSel  int        // row to re-select once restored rows arrive
	flashTimer  *time.Timer
}

// navEntry is one step of navigation history. Like k9s's page stack, every
// view change (":resource", enter on a row, "?") pushes the current state,
// and esc pops back to it — resource, filter and selection included.
type navEntry struct {
	page        string
	res         data.Resource
	filter      string
	stateFilter string
	detailRow   data.Row
	selRow      int
}

func New(o Options) (*App, error) {
	p, startErr := o.Factory(o.Current)
	if startErr != nil {
		// Don't exit: open on the :ctx view so the user can add or fix a
		// context from inside the TUI (first-run experience with no keys).
		site := "-"
		for _, c := range o.Contexts {
			if c.Name == o.Current {
				site = c.Site
			}
		}
		p = data.NewErrored(site, startErr)
	}
	a := &App{
		Application:  tview.NewApplication(),
		provider:     data.NewCached(p),
		opts:         o,
		ctxInfos:     o.Contexts,
		current:      o.Current,
		refreshEvery: o.Refresh,
		queries:      map[string]string{},
	}
	a.build()
	if startErr != nil {
		a.showContexts()
		a.flash("✗ context "+o.Current+": "+startErr.Error()+" — press <a> to add a context", true)
	} else {
		a.switchResource(data.Resources()[0])
	}
	go a.ticker()
	return a, nil
}

func (a *App) build() {
	a.infoTV = tview.NewTextView().SetDynamicColors(true)
	a.hintTV = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)

	header := tview.NewFlex().
		AddItem(a.infoTV, 0, 2, false).
		AddItem(a.hintTV, 0, 3, false)

	a.table = tview.NewTable().
		SetFixed(1, 0).
		SetSelectable(true, false)
	a.table.SetBorder(true)
	a.table.SetBorderColor(tcell.ColorDodgerBlue)
	a.table.SetTitleColor(tcell.ColorOrange)
	a.table.SetSelectedStyle(tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite))
	a.table.SetSelectedFunc(func(row, _ int) { a.openDetail(row) })

	a.prompt = tview.NewInputField().
		SetLabelColor(tcell.ColorOrange).
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(tcell.ColorAqua)
	a.prompt.SetDoneFunc(a.promptDone)
	a.prompt.SetChangedFunc(func(text string) {
		if a.promptM == promptFilter && !a.res.ServerQuery {
			a.filter = text
			a.applyFilter()
		}
	})
	a.prompt.SetAutocompleteFunc(func(current string) []string {
		if a.promptM != promptCmd || current == "" {
			return nil
		}
		var out []string
		for _, r := range data.Resources() {
			if strings.HasPrefix(r.Key, strings.ToLower(current)) {
				out = append(out, r.Key)
			}
		}
		return out
	})
	// Enter on an autocomplete entry executes the command immediately —
	// without this the dropdown swallows the first Enter and ':incidents⏎'
	// leaves the user stuck in the prompt.
	a.prompt.SetAutocompletedFunc(func(text string, _ int, source int) bool {
		if source == tview.AutocompletedNavigate {
			return false
		}
		a.prompt.SetText(text)
		if source == tview.AutocompletedEnter || source == tview.AutocompletedClick {
			a.closePrompt()
			a.execCommand(text)
			return true
		}
		return false
	})

	a.status = tview.NewTextView().SetDynamicColors(true)
	a.footer = tview.NewPages().
		AddPage("status", a.status, true, true).
		AddPage("prompt", a.prompt, true, false)

	a.detail = tview.NewTextView().SetWrap(false)
	a.detail.SetBorder(true)
	a.detail.SetBorderColor(tcell.ColorDodgerBlue)
	a.detail.SetTitleColor(tcell.ColorOrange)

	a.ctxForm = tview.NewForm()
	a.ctxForm.SetBorder(true)
	a.ctxForm.SetTitle(" Add context ")
	a.ctxForm.SetTitleColor(tcell.ColorOrange)
	a.ctxForm.SetBorderColor(tcell.ColorDodgerBlue)
	a.ctxForm.SetFieldBackgroundColor(tcell.ColorDarkSlateGray)
	a.ctxForm.SetButtonBackgroundColor(tcell.ColorDodgerBlue)
	a.ctxForm.SetLabelColor(tcell.ColorOrange)

	a.confirm = tview.NewModal()

	// The add-context form sits beside a guidance panel explaining where
	// each credential comes from, with a validation-error line on top so
	// rejects are visible right where the user is looking (not only in the
	// status bar at the bottom).
	guidance := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	guidance.SetBorder(true).SetTitle(" Guidance ").SetTitleColor(tcell.ColorOrange)
	guidance.SetBorderColor(tcell.ColorDodgerBlue)
	guidance.SetText(ctxFormGuidance)
	a.formErr = tview.NewTextView().SetDynamicColors(true)
	ctxFormFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.formErr, 1, 0, false).
		AddItem(tview.NewFlex().
			AddItem(a.ctxForm, 0, 2, true).
			AddItem(guidance, 0, 3, false), 0, 1, true)

	// The content pane is the only thing that switches; header (with the
	// shortcut hints) and footer stay on screen in every view, k9s-style.
	a.content = tview.NewPages().
		AddPage("table", a.table, true, true).
		AddPage("detail", a.detail, true, false).
		AddPage("help", a.buildHelp(), true, false).
		AddPage("ctxform", ctxFormFlex, true, false).
		AddPage("confirm", a.confirm, true, false)
	a.page = "table"

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(a.content, 0, 1, true).
		AddItem(a.footer, 1, 0, false)

	a.SetInputCapture(a.keys)
	a.SetRoot(main, true).EnableMouse(true)
	a.setHints()
}

// setHints shows the shortcuts valid in the current context, k9s-style.
func (a *App) setHints() {
	var lines []string
	switch a.page {
	case "detail":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<o>[white]open in Datadog  [aqua]<?>[white]help",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<q>[white]back",
		}
	case "help":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<q>[white]back",
		}
	case "ctxform":
		lines = []string{
			"[aqua]<tab>[white]next field  [aqua]<shift-tab>[white]previous",
			"[aqua]<esc>[white]cancel  [aqua]<enter>[white]on Save to store",
		}
	default:
		lines = []string{
			"[aqua]<:>[white]cmd  [aqua]</>[white]filter  [aqua]<enter>[white]details  [aqua]<o>[white]open in Datadog",
			"[aqua]<ctrl-r>[white]refresh  [aqua]<esc>[white]back  [aqua]<?>[white]help  [aqua]<q>[white]quit",
			"",
			"[orange]:monitors  :incidents  :slos  :logs  :dashboards  :ctx",
		}
		switch a.res.Key {
		case "monitors":
			lines = append(lines, "[gray]<l>drill to logs   quick filters: <1>alert <2>warn <3>nodata <4>ok <0>all")
		case ctxResource.Key:
			lines = append(lines, "[gray]<enter>switch org  <a>add  <e>edit config  <ctrl-d>delete")
		}
	}
	a.hintTV.SetText(strings.Join(lines, "\n"))
}

func (a *App) buildHelp() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Help ").SetTitleColor(tcell.ColorOrange)
	fmt.Fprint(tv, `
 [orange]NAVIGATION
   [aqua]:<resource>[white]   switch view (monitors, incidents, slos, logs, dashboards — or mon, inc, s, l, d)
   [aqua]:ctx[white]          list Datadog org contexts; enter switches org (cache, budget and
                 history are dropped — a context is a hard boundary)
   [aqua]a[white]             (in :ctx) add a context: name, site, paste API/APP keys or an
                 access token — stored in the OS keychain, never in the config file
   [aqua]e[white]             (in :ctx) edit the config file in $EDITOR (vi by default),
                 then reload and re-validate it
   [aqua]ctrl-d[white]        (in :ctx) delete the selected context (asks first)
   [aqua]/<text>[white]       filter rows; in Logs this is a Datadog search query sent to the API
   [aqua]↑/↓ j/k[white]       move selection
   [aqua]enter[white]         open detail view — fetches the full object on demand where the
                 list is only a summary (monitors, dashboards, incidents)
   [aqua]esc[white]           go back to the previous view (navigation history, like k9s);
                 clears the active filter on the way out

 [orange]ACTIONS
   [aqua]l[white]             (on a monitor) drill down to its logs: jumps to the Logs view
                 with the monitor's log query, or its service:/env: tags.
                 esc returns to the monitors view
   [aqua]o[white]             open the selected item in the Datadog web UI (also works in detail view)
   [aqua]ctrl-r[white]        force refresh (bypasses cache — spends API budget)
   [aqua]0-4[white]           monitors quick filter: all/alert/warn/nodata/ok

 [orange]OTHER
   [aqua]?[white]             this help (from any view)
   [aqua]q[white]             back in detail/help; quit from a table view
   [aqua]ctrl-c[white]        quit (also :q :quit :exit)

 [gray]Views auto-refresh only where it is cheap (monitors, incidents) and are
 [gray]otherwise cached per TTL. The Budget header shows Datadog rate-limit
 [gray]headroom as reported by X-RateLimit response headers.
`)
	return tv
}

// ---- input ----------------------------------------------------------------

func (a *App) keys(ev *tcell.EventKey) *tcell.EventKey {
	if a.GetFocus() == a.prompt {
		return ev
	}
	switch a.page {
	case "help":
		if ev.Key() == tcell.KeyEscape || ev.Rune() == 'q' {
			a.back()
			return nil
		}
		return ev
	case "ctxform":
		if ev.Key() == tcell.KeyEscape {
			a.back()
			return nil
		}
		return ev // the form handles everything else (typing, tab, buttons)
	case "confirm":
		if ev.Key() == tcell.KeyEscape {
			a.closeConfirm()
			return nil
		}
		return ev // modal buttons handle enter/arrows
	case "detail":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == 'o':
			a.openURL(a.detailRow.URL)
			return nil
		case ev.Rune() == 'l' && a.res.Key == "monitors":
			a.drillToLogs(a.detailRow)
			return nil
		case ev.Rune() == '?':
			a.showHelp()
			return nil
		case ev.Rune() == ':':
			a.openPrompt(promptCmd)
			return nil
		case ev.Rune() == 'j':
			return tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
		case ev.Rune() == 'k':
			return tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)
		}
		return ev
	}
	switch ev.Key() {
	case tcell.KeyCtrlR:
		a.load(true)
		return nil
	case tcell.KeyEscape:
		a.back() // clears the filter and pops to the previous view
		return nil
	}
	switch ev.Rune() {
	case ':':
		a.openPrompt(promptCmd)
		return nil
	case '/':
		a.openPrompt(promptFilter)
		return nil
	case '?':
		a.showHelp()
		return nil
	case 'q':
		a.Stop()
		return nil
	case 'o':
		a.openSelected()
		return nil
	case 'j':
		return tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
	case 'k':
		return tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)
	case 'a':
		if a.res.Key == ctxResource.Key {
			a.openCtxForm()
			return nil
		}
	case 'e':
		if a.res.Key == ctxResource.Key {
			a.editConfig()
			return nil
		}
	case 'l':
		if a.res.Key == "monitors" {
			if r, ok := a.selectedRow(); ok {
				a.drillToLogs(r)
			}
			return nil
		}
	case '0', '1', '2', '3', '4':
		if a.res.Key == "monitors" {
			a.quickFilter(ev.Rune())
			return nil
		}
	}
	if ev.Key() == tcell.KeyCtrlD && a.res.Key == ctxResource.Key {
		a.confirmDeleteContext()
		return nil
	}
	return ev
}

// quickFilter matches the STATE column exactly — a monitor named
// "… Warning Threshold Reached" must NOT match the Warn quick filter.
func (a *App) quickFilter(r rune) {
	switch r {
	case '1':
		a.stateFilter = "Alert"
	case '2':
		a.stateFilter = "Warn"
	case '3':
		a.stateFilter = "No Data"
	case '4':
		a.stateFilter = "OK"
	default:
		a.stateFilter = ""
	}
	a.applyFilter()
}

func (a *App) openPrompt(m promptMode) {
	a.promptM = m
	if m == promptCmd {
		a.prompt.SetLabel(" 🐶 > ")
	} else if a.res.ServerQuery {
		a.prompt.SetLabel(" query> ")
	} else {
		a.prompt.SetLabel(" /")
	}
	a.prompt.SetText("")
	a.footer.SwitchToPage("prompt")
	a.SetFocus(a.prompt)
}

func (a *App) closePrompt() {
	a.promptM = promptNone
	a.footer.SwitchToPage("status")
	if a.page == "detail" {
		a.SetFocus(a.detail)
	} else {
		a.SetFocus(a.table)
	}
}

func (a *App) promptDone(key tcell.Key) {
	text := strings.TrimSpace(a.prompt.GetText())
	mode := a.promptM
	if mode == promptNone {
		return // already handled by the autocomplete path
	}
	a.closePrompt()
	if key == tcell.KeyEscape {
		if mode == promptFilter && !a.res.ServerQuery {
			a.filter = ""
			a.applyFilter()
		}
		return
	}
	if key != tcell.KeyEnter {
		return
	}
	switch mode {
	case promptCmd:
		a.execCommand(text)
	case promptFilter:
		if a.res.ServerQuery {
			a.queries[a.res.Key] = text
			a.load(true)
		} else {
			a.filter = text
			a.applyFilter()
		}
	}
}

func (a *App) execCommand(cmd string) {
	cmd = strings.TrimSpace(strings.TrimPrefix(cmd, ":"))
	switch cmd {
	case "":
		return
	case "q", "quit", "exit":
		a.Stop()
		return
	case "help", "?":
		a.showHelp()
		return
	}
	if cmd == "ctx" || cmd == "context" || cmd == "contexts" {
		a.showContexts()
		return
	}
	if res, ok := data.ResourceByAlias(cmd); ok {
		a.switchResource(res)
		return
	}
	a.flash(fmt.Sprintf("unknown command %q — try :monitors :incidents :slos :logs :dashboards :ctx", cmd), true)
}

// drillToLogs is the k9s killer feature: from a monitor, jump straight to
// its logs — the monitor's own query for log monitors, service:/env: tags
// otherwise. Esc pops back to the monitors view via the navigation stack.
func (a *App) drillToLogs(r data.Row) {
	if r.LogQuery == "" {
		a.flash("no log query derivable from this monitor (no log query or service:/env: tags)", true)
		return
	}
	var logsRes data.Resource
	for _, res := range data.Resources() {
		if res.Key == "logs" {
			logsRes = res
		}
	}
	slog.Info("drill-down monitor→logs", "monitor", r.ID, "query", r.LogQuery)
	a.queries["logs"] = r.LogQuery
	a.switchResource(logsRes) // pushes the current view; esc returns here
}

// showContexts opens the :ctx view listing the configured Datadog orgs.
func (a *App) showContexts() {
	if a.page == "table" && a.res.Key == ctxResource.Key {
		return
	}
	if a.res.Key != "" {
		a.pushNav()
	}
	a.res = ctxResource
	a.filter = ""
	a.stateFilter = ""
	a.rows = nil
	a.filtered = nil
	a.pendingSel = 1
	a.showPage("table")
	a.render()
	a.load(false)
}

func (a *App) contextRows() []data.Row {
	rows := make([]data.Row, 0, len(a.ctxInfos))
	for _, c := range a.ctxInfos {
		active := ""
		if c.Name == a.current {
			active = "*"
		}
		rows = append(rows, data.Row{
			ID:    c.Name,
			Cells: []string{active, c.Name, c.Site, c.Keys},
			Raw:   map[string]any{"name": c.Name, "site": c.Site, "keys": c.Keys},
		})
	}
	return rows
}

// switchContext tears down everything org-specific — provider, cache,
// rate-limit state, navigation history — and starts fresh on the new org.
// Different org means different data and a different API budget; nothing
// may leak across the boundary.
func (a *App) switchContext(name string) {
	if name == a.current {
		a.flash("already on context "+name, false)
		return
	}
	p, err := a.opts.Factory(name)
	if err != nil {
		slog.Error("context switch failed", "to", name, "err", err)
		a.flash("✗ context "+name+": "+err.Error(), true)
		return
	}
	slog.Info("context switch", "from", a.current, "to", name)
	a.provider = data.NewCached(p)
	a.current = name
	a.stack = nil
	a.queries = map[string]string{}
	a.detailRow = data.Row{}
	a.res = data.Resource{} // so switchResource doesn't push the ctx view
	a.flash("context → "+name, false)
	a.switchResource(data.Resources()[0])
}

// pushNav records the current state on the navigation stack.
func (a *App) pushNav() {
	sel, _ := a.table.GetSelection()
	a.stack = append(a.stack, navEntry{
		page:        a.page,
		res:         a.res,
		filter:      a.filter,
		stateFilter: a.stateFilter,
		detailRow:   a.detailRow,
		selRow:      sel,
	})
}

// showHelp opens the help page; esc pops back to wherever the user came from.
func (a *App) showHelp() {
	if a.page == "help" {
		return
	}
	a.pushNav()
	a.showPage("help")
}

// back implements k9s's esc semantics (Browser.resetCmd): clear any active
// filter, then pop the navigation stack to the previous view. At the root
// with no filter, esc is a no-op.
func (a *App) back() {
	if a.page == "table" && (a.filter != "" || a.stateFilter != "") {
		a.filter = ""
		a.stateFilter = ""
		a.applyFilter()
	}
	if len(a.stack) == 0 {
		return
	}
	e := a.stack[len(a.stack)-1]
	a.stack = a.stack[:len(a.stack)-1]
	a.restore(e)
}

// restore re-applies a popped navigation entry.
func (a *App) restore(e navEntry) {
	a.res = e.res
	a.filter = e.filter
	a.stateFilter = e.stateFilter
	a.detailRow = e.detailRow
	switch e.page {
	case "detail":
		a.renderDetail(e.detailRow)
		a.showPage("detail")
	default:
		a.rows = nil
		a.filtered = nil
		a.pendingSel = e.selRow
		a.showPage("table")
		a.render()
		a.load(false) // served from cache unless the TTL expired
	}
}

func (a *App) showPage(page string) {
	a.page = page
	a.content.SwitchToPage(page)
	switch page {
	case "detail":
		a.SetFocus(a.detail) // focus so ↑/↓ scroll the JSON
	case "ctxform":
		a.SetFocus(a.ctxForm)
	default:
		a.SetFocus(a.table)
	}
	a.setHints()
	a.updateInfo()
}

// ---- context add / delete ---------------------------------------------------

// siteRegions annotates config.Sites (the single source of truth — it is a
// security allowlist there) with human-readable regions for the dropdown.
var siteRegions = map[string]string{
	"datadoghq.com":     "US1",
	"datadoghq.eu":      "EU",
	"us3.datadoghq.com": "US3",
	"us5.datadoghq.com": "US5",
	"ap1.datadoghq.com": "AP1",
	"ap2.datadoghq.com": "AP2",
	"ddog-gov.com":      "US1-FED",
}

// openCtxForm shows the add-context form (:ctx → a). Secret fields are
// masked; in live mode the credentials are stored in the OS keychain,
// never in the config file.
func (a *App) openCtxForm() {
	if a.opts.AddContext == nil {
		a.flash("adding contexts is not available in this mode", true)
		return
	}
	a.pushNav()
	a.formErr.SetText("")
	labels := make([]string, len(config.Sites))
	for i, s := range config.Sites {
		labels[i] = fmt.Sprintf("%-17s (%s)", s, siteRegions[s])
	}
	a.ctxForm.Clear(true)
	a.ctxForm.
		AddInputField("Name", "", 30, nil, nil).
		AddDropDown("Site", labels, 0, nil).
		AddPasswordField("API key (option 1)", "", 50, '*', nil).
		AddPasswordField("APP key (option 1)", "", 50, '*', nil).
		AddPasswordField("Access token (option 2)", "", 50, '*', nil).
		AddInputField("Subdomain (optional)", "", 30, nil, nil).
		AddButton("Save", a.saveCtxForm).
		AddButton("Cancel", a.back)
	a.showPage("ctxform")
}

// formError shows a validation error inside the form page (and logs it) —
// the bottom status bar alone is too easy to miss while filling fields.
func (a *App) formError(msg string) {
	slog.Warn("add-context form rejected", "reason", msg)
	a.formErr.SetText(" [red::b]✗ " + tview.Escape(msg))
}

// ctxFormGuidance is shown next to the add-context form so developers know
// where each credential comes from. Lines are kept short and unindented so
// word-wrap degrades gracefully in narrow terminals.
const ctxFormGuidance = `[orange]How to fill this in[white]

[aqua]Name[white] — anything you like ("Datadog Dev", "prod", …).

[aqua]Site[white] — pick from the list (enter/space or click opens it). It matches the region in your Datadog URL: app.[green]datadoghq.eu[white] → datadoghq.eu.

[aqua]Credentials — the fields are optional individually; fill exactly ONE option:[white]

[yellow]Option 1) API key + APP key[white] (recommended for daily use)
[green]API key[white]: Organization Settings → API Keys.
Org-wide; ask an admin if you cannot create one.
[green]APP key[white]: Personal Settings → Application Keys → New Key.
Scope it read-only: monitors_read, incidents_read, slos_read, logs_read_data, dashboards_read.

[yellow]Option 2) Access token only[white]
A bearer token (OAuth2 access token or PAT), e.g. from Datadog's pup CLI or your SSO tooling. Leave both key fields empty. Tokens are usually short-lived (~1h).

[aqua]Subdomain[white] — only if your org's web UI lives on a custom subdomain: for https://[green]acme-stage[white].datadoghq.eu enter [green]acme-stage[white]. Fixes 'open in Datadog' links; API calls are unaffected. Leave empty if your URL starts with app.

[gray]Secrets go to the OS keychain (service "ike"), never into the config file. <esc> cancels.`

func (a *App) saveCtxForm() {
	name := strings.TrimSpace(a.ctxForm.GetFormItem(0).(*tview.InputField).GetText())
	siteIdx, _ := a.ctxForm.GetFormItem(1).(*tview.DropDown).GetCurrentOption()
	if siteIdx < 0 || siteIdx >= len(config.Sites) {
		siteIdx = 0
	}
	site := config.Sites[siteIdx]
	apiKey := a.ctxForm.GetFormItem(2).(*tview.InputField).GetText()
	appKey := a.ctxForm.GetFormItem(3).(*tview.InputField).GetText()
	token := a.ctxForm.GetFormItem(4).(*tview.InputField).GetText()
	subdomain := strings.TrimSpace(a.ctxForm.GetFormItem(5).(*tview.InputField).GetText())

	if name == "" {
		a.formError("Name is required")
		return
	}
	if !config.ValidSubdomain(subdomain) {
		a.formError("subdomain must be a single DNS label, e.g. acme-stage (from https://acme-stage." + site + ")")
		return
	}
	for _, c := range a.ctxInfos {
		if c.Name == name {
			a.formError("context " + name + " already exists")
			return
		}
	}
	auth := "key pair"
	hasPair := apiKey != "" || appKey != ""
	switch {
	case token != "" && hasPair:
		a.formError("fill either the API+APP key pair OR an access token — not both")
		return
	case token == "" && (apiKey == "" || appKey == ""):
		a.formError("credentials missing: fill BOTH keys of option 1, or only the access token (option 2)")
		return
	case token != "":
		auth = "token"
	}
	info, err := a.opts.AddContext(name, site, apiKey, appKey, token, subdomain)
	if err != nil {
		a.formError(err.Error())
		return
	}
	slog.Info("context added", "name", name, "site", site, "auth", auth)
	a.ctxInfos = append(a.ctxInfos, info)
	a.back() // pop to the :ctx table, which re-reads ctxInfos
	a.flash("context "+name+" added — enter on it to switch", false)
}

// editConfig suspends the TUI and opens the config file in $EDITOR
// (k9s-style 'e'), then reloads and re-validates it.
func (a *App) editConfig() {
	if a.opts.ConfigPath == "" || a.opts.ReloadContexts == nil {
		a.flash("no config file to edit in this mode", true)
		return
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	var runErr error
	a.Suspend(func() {
		cmd := exec.Command(editor, a.opts.ConfigPath)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr = cmd.Run()
	})
	if runErr != nil {
		slog.Error("editor failed", "editor", editor, "err", runErr)
		a.flash("✗ "+editor+": "+runErr.Error(), true)
		return
	}
	infos, err := a.opts.ReloadContexts()
	if err != nil {
		slog.Error("config reload failed", "err", err)
		a.flash("✗ config not reloaded: "+err.Error(), true)
		return
	}
	slog.Info("config reloaded after edit", "contexts", len(infos))
	a.ctxInfos = infos
	found := false
	for _, c := range infos {
		if c.Name == a.current {
			found = true
		}
	}
	a.load(false)
	if !found {
		a.flash("config reloaded — note: active context "+a.current+" is no longer defined", true)
		return
	}
	a.flash("config reloaded", false)
}

// confirmDeleteContext asks before removing the selected context (ctrl-d).
func (a *App) confirmDeleteContext() {
	if a.opts.DeleteContext == nil {
		a.flash("deleting contexts is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	name := r.ID
	if name == a.current {
		a.flash("✗ cannot delete the active context — switch away first", true)
		return
	}
	a.confirm.
		SetText(fmt.Sprintf("Delete context %q?\nIts credentials are removed from the OS keychain;\nthe Datadog org itself is untouched.", name)).
		ClearButtons().
		AddButtons([]string{"Cancel", "Delete"}).
		SetDoneFunc(func(_ int, label string) {
			a.closeConfirm()
			if label != "Delete" {
				return
			}
			if err := a.opts.DeleteContext(name); err != nil {
				slog.Error("context delete failed", "name", name, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			slog.Info("context deleted", "name", name)
			for i, c := range a.ctxInfos {
				if c.Name == name {
					a.ctxInfos = append(a.ctxInfos[:i], a.ctxInfos[i+1:]...)
					break
				}
			}
			a.load(false) // re-render the contexts table
			a.flash("context "+name+" deleted", false)
		})
	a.page = "confirm"
	a.content.ShowPage("confirm") // overlay on top of the table
	a.SetFocus(a.confirm)
	a.setHints()
}

func (a *App) closeConfirm() {
	a.content.HidePage("confirm")
	a.page = "table"
	a.SetFocus(a.table)
	a.setHints()
}

// ---- data flow -------------------------------------------------------------

func (a *App) switchResource(res data.Resource) {
	if a.page == "table" && res.Key == a.res.Key {
		return // ':monitors' while on monitors — nothing to do
	}
	if a.res.Key != "" {
		a.pushNav() // k9s-style: goto pushes, esc pops back here
	}
	a.res = res
	a.filter = ""
	a.stateFilter = ""
	if res.ServerQuery && a.queries[res.Key] == "" {
		a.queries[res.Key] = res.DefaultQuery
	}
	a.rows = nil
	a.filtered = nil
	a.pendingSel = 1 // a freshly switched view starts at the top
	a.showPage("table")
	a.render()
	a.load(false)
}

func (a *App) load(force bool) {
	if a.res.Key == ctxResource.Key {
		// The :ctx view is served from the app's own config, not a Provider.
		a.rows = a.contextRows()
		a.fetchedAt = time.Now()
		a.applyFilter()
		return
	}
	if a.loading {
		return
	}
	a.loading = true
	res, q := a.res, a.queries[a.res.Key]
	go func() {
		start := time.Now()
		rows, at, cached, err := a.provider.Fetch(context.Background(), res, q, force)
		slog.Debug("fetch", "resource", res.Key, "query", q, "force", force,
			"rows", len(rows), "cached", cached, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			a.loading = false
			if a.res.Key != res.Key {
				return // user switched view while fetching
			}
			if err != nil {
				a.flash("✗ "+err.Error(), true)
				if rows == nil {
					return
				}
			}
			a.rows = rows
			a.fetchedAt = at
			a.applyFilter()
			if err == nil {
				src := "api"
				if cached {
					src = "cache"
				}
				a.flash(fmt.Sprintf("%s: %d items (%s)", res.Title, len(rows), src), false)
			}
		})
	}()
}

func (a *App) applyFilter() {
	a.filtered = a.filtered[:0]
	for i, r := range a.rows {
		if matchRow(r, a.stateFilter, a.filter) {
			a.filtered = append(a.filtered, i)
		}
	}
	a.render()
}

// matchRow applies both filters: state is an exact (case-insensitive) match
// on the first column; text is a substring match across all cells.
func matchRow(r data.Row, state, text string) bool {
	if state != "" && (len(r.Cells) == 0 || !strings.EqualFold(r.Cells[0], state)) {
		return false
	}
	if text == "" {
		return true
	}
	t := strings.ToLower(text)
	for _, c := range r.Cells {
		if strings.Contains(strings.ToLower(c), t) {
			return true
		}
	}
	return false
}

func (a *App) ticker() {
	t := time.NewTicker(a.refreshEvery)
	defer t.Stop()
	for range t.C {
		if a.res.AutoRefresh && !a.loading {
			a.QueueUpdateDraw(func() { a.load(false) })
		} else {
			a.QueueUpdateDraw(a.updateInfo) // keep the Age counter moving
		}
	}
}

// ---- rendering -------------------------------------------------------------

func (a *App) render() {
	prevRow, _ := a.table.GetSelection()
	a.table.Clear()
	for c, col := range a.res.Columns {
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
		for c, val := range r.Cells {
			if len(val) > 200 {
				val = val[:197] + "…"
			}
			cell := tview.NewTableCell(tview.Escape(val)).
				SetTextColor(color).
				SetExpansion(expansion(a.res.Columns[c]))
			a.table.SetCell(n+1, c, cell)
		}
	}
	var parts []string
	if a.stateFilter != "" {
		parts = append(parts, "state:"+a.stateFilter)
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
	a.table.SetTitle(tview.Escape(fmt.Sprintf(" %s(%s)[%d] ", a.res.Title, flabel, len(a.filtered))))
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
	budget := strings.Join(a.provider.Budget(), " • ")
	if budget == "" {
		budget = "n/a (no API calls yet)"
	}
	// Escape: context names like "staging" would otherwise parse as tview
	// color tags inside this dynamic-colors TextView and vanish.
	mode := tview.Escape(fmt.Sprintf("%s [%s]", a.provider.Mode(), a.current))
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
	a.infoTV.SetText(fmt.Sprintf(
		" [orange]Mode:[%s]   %s\n [orange]Site:[white]   %s\n [orange]View:[white]   %s\n [orange]Age:[white]    %s\n [orange]Budget:[gray] %s",
		modeColor, mode, a.provider.Site(), view, age, tview.Escape(budget)))
}

func (a *App) flash(msg string, isErr bool) {
	color := "[green]"
	if isErr {
		color = "[red]"
	}
	a.status.SetText(color + tview.Escape(msg))
	if a.flashTimer != nil {
		a.flashTimer.Stop()
	}
	a.flashTimer = time.AfterFunc(5*time.Second, func() {
		a.QueueUpdateDraw(func() { a.status.SetText("") })
	})
}

// ---- detail / actions -------------------------------------------------------

func (a *App) selectedRow() (data.Row, bool) {
	row, _ := a.table.GetSelection()
	if row < 1 || row > len(a.filtered) {
		return data.Row{}, false
	}
	return a.rows[a.filtered[row-1]], true
}

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
	a.pushNav()
	a.renderDetail(r)
	a.detailRow = r
	a.showPage("detail")

	// The list row is often only a summary (dashboards have no widgets in
	// the listing) — upgrade to the full object on demand, in background,
	// and swap it in if the user is still looking at this row.
	res := a.res
	go func() {
		full, err := a.provider.FetchDetail(context.Background(), res.Key, r.ID)
		if err != nil {
			slog.Warn("detail fetch failed", "resource", res.Key, "id", r.ID, "err", err)
			a.QueueUpdateDraw(func() { a.flash("✗ full object: "+err.Error(), true) })
			return
		}
		if full == nil {
			return // the row already was the complete object
		}
		slog.Debug("detail fetched", "resource", res.Key, "id", r.ID)
		a.QueueUpdateDraw(func() {
			if a.page != "detail" || a.detailRow.ID != r.ID {
				return // user navigated away meanwhile
			}
			b, err := json.MarshalIndent(full, "", "  ")
			if err != nil {
				return
			}
			row, col := a.detail.GetScrollOffset()
			a.detail.SetText(string(b))
			a.detail.ScrollTo(row, col)
			a.flash("full object loaded", false)
		})
	}()
}

func (a *App) renderDetail(r data.Row) {
	b, err := json.MarshalIndent(r.Raw, "", "  ")
	if err != nil {
		b = []byte("✗ " + err.Error())
	}
	a.detail.SetText(string(b)).ScrollToBeginning()
	a.detail.SetTitle(fmt.Sprintf(" %s/%s ", strings.TrimSuffix(a.res.Title, "s"), r.ID))
}

func (a *App) openSelected() {
	r, ok := a.selectedRow()
	if !ok {
		a.flash("nothing to open", true)
		return
	}
	a.openURL(r.URL)
}

func (a *App) openURL(url string) {
	if url == "" {
		a.flash("nothing to open", true)
		return
	}
	// URLs are built from API response data; refuse anything that is not
	// plain https so a hostile payload can't reach `open` with a file://,
	// javascript: or custom-scheme URL.
	if !strings.HasPrefix(url, "https://") {
		slog.Warn("refused to open non-https URL", "url", url)
		a.flash("✗ refusing to open non-https URL", true)
		return
	}
	if err := openBrowser(url); err != nil {
		a.flash("✗ "+err.Error(), true)
		return
	}
	a.flash("opened "+url, false)
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "linux":
		return exec.Command("xdg-open", u).Start()
	default:
		return fmt.Errorf("open not supported on %s — %s", runtime.GOOS, u)
	}
}
