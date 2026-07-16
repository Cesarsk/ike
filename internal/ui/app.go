package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
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

	content  *tview.Pages // "table" | "detail" | "help" | "ctxform" (+ "confirm" overlay)
	infoTV   *tview.TextView
	hintTV   *tview.TextView
	table    *tview.Table
	prompt   *tview.InputField
	status   *tview.TextView
	footer   *tview.Pages
	detail   *tview.TextView
	dash     *tview.TextView
	trace    *tview.TextView
	patterns *tview.TextView
	ctxForm  *tview.Form
	formErr  *tview.TextView
	confirm  *tview.Modal

	res      data.Resource
	rows     []data.Row
	filtered []int
	filter   string // '/' text filter: substring across all cells
	// colFilter is the exact-match quick filter (monitors state via 0-4,
	// SLO type via 't'): matches one column exactly. colFilterCol == -1 off.
	colFilterCol int
	colFilterVal string
	// sortCol == -1 means the resource's natural order; otherwise sort the
	// filtered rows by that column, direction sortAsc.
	sortCol    int
	sortAsc    bool
	queries    map[string]string   // per-resource server-side query (logs)
	history    map[string][]string // per-resource submitted-query history (↑/↓ recall)
	histIdx    int                 // cursor into the current resource's history
	logRangeIx int                 // index into logRanges for the Logs time window
	fetchedAt  time.Time
	loading    bool
	paused     bool // auto-refresh paused (toggled with 'p')
	promptM    promptMode
	page       string // current content page: "table", "help", "detail"
	detailRow  data.Row
	stack      []navEntry // navigation history, k9s-style: esc pops
	pendingSel int        // row to re-select once restored rows arrive
	flashTimer *time.Timer
}

// navEntry is one step of navigation history. Like k9s's page stack, every
// view change (":resource", enter on a row, "?") pushes the current state,
// and esc pops back to it — resource, filter and selection included.
type navEntry struct {
	page         string
	res          data.Resource
	filter       string
	colFilterCol int
	colFilterVal string
	sortCol      int
	sortAsc      bool
	query        string // server query (a.queries[res.Key]) at push time
	detailRow    data.Row
	selRow       int
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
		history:      map[string][]string{},
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
		switch {
		case a.promptM == promptCmd && current != "":
			var out []string
			for _, r := range data.Resources() {
				if strings.HasPrefix(r.Key, strings.ToLower(current)) {
					out = append(out, r.Key)
				}
			}
			return out
		case a.promptM == promptFilter && a.res.Key == "logs":
			return a.logQueryCompletions(current)
		}
		return nil
	})
	// Command mode: Enter on an entry executes immediately (without this the
	// dropdown swallows the first Enter). Logs query mode: Enter/Tab accepts
	// the completion into the field but does NOT submit — the user keeps
	// composing the query and submits with a second Enter (DoneFunc).
	a.prompt.SetAutocompletedFunc(func(text string, _ int, source int) bool {
		if source == tview.AutocompletedNavigate {
			return false
		}
		a.prompt.SetText(text)
		if a.promptM == promptCmd {
			if source == tview.AutocompletedEnter || source == tview.AutocompletedClick {
				a.closePrompt()
				a.execCommand(text)
			}
			return true
		}
		// logs query mode: accept the token, keep composing
		return source == tview.AutocompletedEnter || source == tview.AutocompletedTab || source == tview.AutocompletedClick
	})

	a.status = tview.NewTextView().SetDynamicColors(true)
	a.footer = tview.NewPages().
		AddPage("status", a.status, true, true).
		AddPage("prompt", a.prompt, true, false)

	a.detail = tview.NewTextView().SetWrap(false)
	a.detail.SetBorder(true)
	a.detail.SetBorderColor(tcell.ColorDodgerBlue)
	a.detail.SetTitleColor(tcell.ColorOrange)

	a.dash = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.dash.SetBorder(true)
	a.dash.SetBorderColor(tcell.ColorDodgerBlue)
	a.dash.SetTitleColor(tcell.ColorOrange)

	a.trace = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.trace.SetBorder(true)
	a.trace.SetBorderColor(tcell.ColorDodgerBlue)
	a.trace.SetTitleColor(tcell.ColorOrange)

	a.patterns = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.patterns.SetBorder(true)
	a.patterns.SetBorderColor(tcell.ColorDodgerBlue)
	a.patterns.SetTitleColor(tcell.ColorOrange)

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
		AddPage("dashboard", a.dash, true, false).
		AddPage("trace", a.trace, true, false).
		AddPage("patterns", a.patterns, true, false).
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
			"[aqua]<esc>[white]back  [aqua]<o>[white]open in Datadog  [aqua]<c>[white]copy  [aqua]<?>[white]help",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<q>[white]back",
		}
	case "dashboard":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<ctrl-r>[white]refresh sparklines  [aqua]<o>[white]open in Datadog",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help  [aqua]<q>[white]back",
		}
	case "trace":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<l>[white]logs for this trace  [aqua]<o>[white]open in Datadog",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help  [aqua]<q>[white]back",
		}
	case "patterns":
		lines = []string{
			"[aqua]<esc>[white]back to logs  [aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help",
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
		refresh := "on"
		if a.paused {
			refresh = "off"
		}
		lines = []string{
			"[aqua]<:>[white]cmd  [aqua]</>[white]filter  [aqua]<enter>[white]details  [aqua]<o>[white]open  [aqua]<c>[white]copy",
			fmt.Sprintf("[aqua]<ctrl-r>[white]refresh  [aqua]<p>[white]auto:%s  [aqua]<esc>[white]back  [aqua]<?>[white]help  [aqua]<q>[white]quit", refresh),
			"",
			"[orange]:monitors :incidents :slos :logs :traces :events :dashboards :ctx",
		}
		switch a.res.Key {
		case "monitors":
			lines = append(lines, "[gray]<l>logs  <m>mute  <s>sort <S>rev   quick: <1>alert <2>warn <3>nodata <4>ok <0>all")
		case "slos":
			lines = append(lines, "[gray]<enter>error budget  <t>cycle type filter  <s>sort <S>reverse")
		case "incidents":
			lines = append(lines, "[gray]<r>change state  quick: <1>active <2>stable <3>resolved <0>all  <s>sort")
		case "logs":
			lines = append(lines, "[gray]</>query (tab=complete, ↑ history)  <t>trace  <P>patterns  window: <1>15m..<5>7d")
		case "traces":
			lines = append(lines, "[gray]</>query  <t>trace waterfall  <l>logs for trace  window: <1>15m..<5>7d  <s>sort")
		case "events":
			lines = append(lines, "[gray]</>query  window: <1>15m..<5>7d  <s>sort   (deploys, alerts, changes)")
		case ctxResource.Key:
			lines = append(lines, "[gray]<enter>switch org  <a>add  <e>edit config  <ctrl-d>delete")
		default:
			lines = append(lines, "[gray]<s>sort <S>reverse")
		}
	}
	a.hintTV.SetText(strings.Join(lines, "\n"))
}

func (a *App) buildHelp() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Help ").SetTitleColor(tcell.ColorOrange)
	fmt.Fprint(tv, `
 [orange]NAVIGATION
   [aqua]:<resource>[white]   switch view (monitors, incidents, slos, logs, traces, events, dashboards)
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
                 list is only a summary (monitors, incidents). On a dashboard,
                 renders its widgets with sparklines (ctrl-r refreshes)
   [aqua]esc[white]           go back to the previous view (navigation history, like k9s);
                 clears the active filter on the way out

 [orange]SORT & FILTER
   [aqua]s[white]             cycle the sort column; [aqua]S[white] reverses direction
   [aqua]0-4[white]           (monitors) quick filter by state: all/alert/warn/nodata/ok
   [aqua]t[white]             (SLOs) cycle the Type filter: metric / monitor / time_slice / all

 [orange]ACTIONS & CORRELATION
   [aqua]l[white]             drill to logs — (monitor) its log query; (trace) that trace's logs
   [aqua]t[white]             drill to the trace waterfall — (logs/traces) the row's trace_id;
                 needs APM log-injection, else "no trace_id" (SLOs: t = type filter)
   [aqua]P[white]             (logs) cluster the loaded lines into patterns — flood triage
   [aqua]m[white]             (monitor) mute / unmute — behind a confirmation
   [aqua]r[white]             (incident) change state (active/stable/resolved) — behind a confirm
   [aqua]c[white]             copy the row's URL / query / id to the clipboard
   [aqua]o[white]             open the selected item in the Datadog web UI (also works in detail view)
   [aqua]ctrl-r[white]        force refresh (bypasses cache — spends API budget)
   [aqua]p[white]             pause / resume auto-refresh

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
		// ↑/↓ recall previously submitted queries/filters for this resource.
		if a.promptM == promptFilter && (ev.Key() == tcell.KeyUp || ev.Key() == tcell.KeyDown) {
			a.recallHistory(ev.Key() == tcell.KeyUp)
			return nil
		}
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
		case ev.Rune() == 'c':
			a.copyRow(a.detailRow)
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
	case "dashboard":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Key() == tcell.KeyCtrlR:
			a.loadDashboard(a.detailRow, true)
			return nil
		case ev.Rune() == 'o':
			a.openURL(a.detailRow.URL)
			return nil
		case ev.Rune() == 'c':
			a.copyRow(a.detailRow)
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
	case "patterns":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
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
	case "trace":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == 'o':
			a.openURL(a.detailRow.URL)
			return nil
		case ev.Rune() == 'l':
			a.drillToLogs(a.detailRow) // trace → its logs (trace_id query)
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
		// monitors → its logs; traces → the trace's logs (trace_id query)
		if a.res.Key == "monitors" || a.res.Key == "traces" {
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
		if a.res.Key == "logs" || a.res.Key == "traces" || a.res.Key == "events" {
			a.setLogRange(ev.Rune())
			return nil
		}
		if a.res.Key == "incidents" {
			a.incidentQuickFilter(ev.Rune())
			return nil
		}
	case '5':
		if a.res.Key == "logs" || a.res.Key == "traces" || a.res.Key == "events" {
			a.setLogRange(ev.Rune())
			return nil
		}
	case 'c':
		a.copySelected()
		return nil
	case 'm':
		if a.res.Key == "monitors" {
			if row, ok := a.selectedRow(); ok {
				a.confirmMuteMonitor(row)
			}
			return nil
		}
	case 'p':
		a.toggleAutoRefresh()
		return nil
	case 't':
		if a.res.Key == "slos" {
			a.cycleSLOType()
			return nil
		}
		// logs/traces → the distributed trace waterfall for this row.
		if a.res.Key == "logs" || a.res.Key == "traces" {
			if r, ok := a.selectedRow(); ok {
				a.drillToTrace(r)
			}
			return nil
		}
	case 's':
		if a.res.Key != ctxResource.Key {
			a.cycleSort()
			return nil
		}
	case 'S':
		if a.res.Key != ctxResource.Key {
			a.toggleSortDir()
			return nil
		}
	case 'P':
		if a.res.Key == "logs" {
			a.showPatterns()
			return nil
		}
	case 'r':
		if a.res.Key == "incidents" {
			if row, ok := a.selectedRow(); ok {
				a.confirmIncidentAction(row)
			}
			return nil
		}
	}
	if ev.Key() == tcell.KeyCtrlD && a.res.Key == ctxResource.Key {
		a.confirmDeleteContext()
		return nil
	}
	return ev
}

// quickFilter matches the STATE column (0) exactly — a monitor named
// "… Warning Threshold Reached" must NOT match the Warn quick filter.
func (a *App) quickFilter(r rune) {
	val := ""
	switch r {
	case '1':
		val = "Alert"
	case '2':
		val = "Warn"
	case '3':
		val = "No Data"
	case '4':
		val = "OK"
	}
	a.setColFilter(0, val)
}

// incidentQuickFilter filters incidents by STATE (column 2): 1 active,
// 2 stable, 3 resolved, 0 all.
func (a *App) incidentQuickFilter(r rune) {
	val := ""
	switch r {
	case '1':
		val = "active"
	case '2':
		val = "stable"
	case '3':
		val = "resolved"
	}
	a.setColFilter(2, val)
}

// copySelected copies the selected table row to the clipboard.
func (a *App) copySelected() {
	if r, ok := a.selectedRow(); ok {
		a.copyRow(r)
	} else {
		a.flash("nothing to copy", true)
	}
}

// copyRow copies a row's most useful identifier to the OS clipboard: the
// Datadog web URL if present, else the log query, else the ID.
func (a *App) copyRow(r data.Row) {
	val, what := r.URL, "URL"
	if val == "" && r.LogQuery != "" {
		val, what = r.LogQuery, "log query"
	}
	if val == "" {
		val, what = r.ID, "id"
	}
	if val == "" {
		a.flash("nothing to copy", true)
		return
	}
	if err := copyClipboard(val); err != nil {
		a.flash("✗ copy: "+err.Error(), true)
		return
	}
	slog.Debug("copied to clipboard", "what", what)
	a.flash("copied "+what+" to clipboard", false)
}

// confirmMuteMonitor asks before muting/unmuting the selected monitor. Mute
// state comes from Row.Muted (the monitor's silenced option), which is
// independent of overall_state.
func (a *App) confirmMuteMonitor(r data.Row) {
	verb := "Mute"
	if r.Muted {
		verb = "Unmute"
	}
	name := ""
	if len(r.Cells) > 2 {
		name = r.Cells[2] // NAME column (after STATE, MUTED)
	}
	a.showConfirm(
		fmt.Sprintf("%s monitor in [%s]?\n\n%s\n\nMuting stops notifications (unmute resumes them); the monitor definition itself is unchanged.",
			verb, a.current, name),
		[]string{"Cancel", verb},
		func(label string) {
			if label != verb {
				return
			}
			go func() {
				err := a.provider.SetMonitorMute(context.Background(), r.ID, !r.Muted)
				a.QueueUpdateDraw(func() {
					if err != nil {
						a.flash("✗ "+err.Error(), true)
						return
					}
					a.flash(verb+"d "+name, false)
					a.load(true)
				})
			}()
		})
}

// logRanges are the selectable Logs time windows (digit keys 1-5 in Logs).
var logRanges = []struct {
	key   rune
	label string
	from  string
}{
	{'1', "15m", "now-15m"},
	{'2', "1h", "now-1h"},
	{'3', "4h", "now-4h"},
	{'4', "1d", "now-1d"},
	{'5', "7d", "now-7d"},
}

// timeRange is the Datadog "from" for the current view — only Logs uses one.
func (a *App) timeRange() string {
	if a.res.Key != "logs" {
		return ""
	}
	return logRanges[a.logRangeIx].from
}

// setLogRange picks a Logs time window by its digit key and refetches
// (the window is part of the cache key, so this always hits the API).
func (a *App) setLogRange(r rune) {
	for i, lr := range logRanges {
		if lr.key == r {
			if a.logRangeIx == i {
				return
			}
			a.logRangeIx = i
			a.flash("logs window: "+lr.label, false)
			a.load(true)
			return
		}
	}
}

// setColFilter sets (or clears, val=="") the exact-match column quick filter.
func (a *App) setColFilter(col int, val string) {
	if val == "" {
		a.colFilterCol, a.colFilterVal = -1, ""
	} else {
		a.colFilterCol, a.colFilterVal = col, val
	}
	a.applyFilter()
}

// cycleSLOType cycles the SLO Type-column filter through the types present.
func (a *App) cycleSLOType() {
	order := []string{"metric", "monitor", "time_slice"}
	// only offer types that actually appear
	present := map[string]bool{}
	for _, r := range a.rows {
		if len(r.Cells) > 1 {
			present[strings.ToLower(r.Cells[1])] = true
		}
	}
	var avail []string
	for _, t := range order {
		if present[t] {
			avail = append(avail, t)
		}
	}
	if len(avail) == 0 {
		a.flash("no SLO types to filter", true)
		return
	}
	// advance from the current selection, wrapping back to "all"
	cur := strings.ToLower(a.colFilterVal)
	next := avail[0]
	for i, t := range avail {
		if t == cur {
			if i+1 >= len(avail) {
				next = "" // wrap to all
			} else {
				next = avail[i+1]
			}
			break
		}
	}
	a.setColFilter(1, next)
}

// cycleSort advances the sort column across the current resource's columns,
// wrapping from the last column back to natural order.
func (a *App) cycleSort() {
	n := len(a.res.Columns)
	if n == 0 {
		return
	}
	if a.sortCol < 0 {
		a.sortCol, a.sortAsc = 0, true
	} else if a.sortCol >= n-1 {
		a.sortCol = -1 // back to natural order
	} else {
		a.sortCol++
	}
	a.applyFilter()
	if a.sortCol >= 0 {
		a.flash(fmt.Sprintf("sort: %s %s", a.res.Columns[a.sortCol], arrow(a.sortAsc)), false)
	} else {
		a.flash("sort: default", false)
	}
}

func (a *App) toggleSortDir() {
	if a.sortCol < 0 {
		a.sortCol, a.sortAsc = 0, true
	} else {
		a.sortAsc = !a.sortAsc
	}
	a.applyFilter()
	a.flash(fmt.Sprintf("sort: %s %s", a.res.Columns[a.sortCol], arrow(a.sortAsc)), false)
}

func arrow(asc bool) string {
	if asc {
		return "▲"
	}
	return "▼"
}

func (a *App) openPrompt(m promptMode) {
	a.promptM = m
	prefill := ""
	if m == promptCmd {
		a.prompt.SetLabel(" 🐶 > ")
	} else if a.res.ServerQuery {
		a.prompt.SetLabel(" query> ")
		prefill = a.queries[a.res.Key] // edit the current query, don't retype
		if prefill == "*" {
			prefill = ""
		}
	} else {
		a.prompt.SetLabel(" /")
	}
	a.prompt.SetText(prefill)
	a.histIdx = len(a.history[a.res.Key]) // ↑ starts at the most recent entry
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
		a.recordHistory(text)
		if a.res.ServerQuery {
			a.queries[a.res.Key] = text
			a.load(true)
		} else {
			a.filter = text
			a.applyFilter()
		}
	}
}

// recordHistory appends a submitted query/filter to this resource's history
// (skipping empties and consecutive duplicates), capped to the last 50.
func (a *App) recordHistory(text string) {
	if text == "" {
		return
	}
	h := a.history[a.res.Key]
	if len(h) > 0 && h[len(h)-1] == text {
		return
	}
	h = append(h, text)
	if len(h) > 50 {
		h = h[len(h)-50:]
	}
	a.history[a.res.Key] = h
}

// recallHistory moves the history cursor and puts that entry in the prompt.
func (a *App) recallHistory(up bool) {
	h := a.history[a.res.Key]
	if len(h) == 0 {
		return
	}
	if up {
		a.histIdx--
	} else {
		a.histIdx++
	}
	switch {
	case a.histIdx < 0:
		a.histIdx = 0
	case a.histIdx >= len(h): // past the newest → empty draft line
		a.histIdx = len(h)
		a.prompt.SetText("")
		return
	}
	a.prompt.SetText(h[a.histIdx])
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
	query := r.LogQuery
	if query == "" && r.TraceID != "" {
		query = "trace_id:" + r.TraceID // from a trace/span with no derived query
	}
	if query == "" {
		a.flash("no log query derivable (no monitor scope / service tags / trace_id)", true)
		return
	}
	var logsRes data.Resource
	for _, res := range data.Resources() {
		if res.Key == "logs" {
			logsRes = res
		}
	}
	slog.Info("drill-down →logs", "from", a.res.Key, "id", r.ID, "query", query)
	a.queries["logs"] = query
	a.switchResource(logsRes) // pushes the current view; esc returns here
}

// drillToTrace opens the distributed-trace waterfall for a log or span row.
// The correlation hinges on trace_id: a log with none can't be correlated.
func (a *App) drillToTrace(r data.Row) {
	if r.TraceID == "" {
		a.flash("no trace_id on this row — is APM log-injection enabled for this service?", true)
		return
	}
	a.pushNav()
	a.detailRow = r
	a.loadTrace(r.TraceID)
}

// showPatterns clusters the currently-loaded log messages into templates
// (zero-API — over the loaded sample, not the full window) as a triage aid.
func (a *App) showPatterns() {
	msgs := make([]string, 0, len(a.rows))
	for _, r := range a.rows {
		if len(r.Cells) > 4 { // MESSAGE column
			msgs = append(msgs, r.Cells[4])
		}
	}
	pats := data.ClusterLogs(msgs)
	a.pushNav()
	a.patterns.SetText(renderPatterns(pats, len(msgs))).ScrollToBeginning()
	a.patterns.SetTitle(fmt.Sprintf(" Log patterns [%d] ", len(pats)))
	a.showPage("patterns")
}

// renderPatterns lists clusters most-frequent first: count, template, example.
func renderPatterns(pats []data.LogPattern, sampled int) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]%d patterns[-:-:-] [gray]over %d loaded log lines · <esc> back[-]\n", len(pats), sampled)
	b.WriteString(" [gray]patterns are computed over the loaded sample, not the full window[-]\n\n")
	if len(pats) == 0 {
		b.WriteString(" [gray]no log lines loaded — run a query in the Logs view first[-]\n")
		return b.String()
	}
	maxN := pats[0].Count
	for _, p := range pats {
		bar := strings.Repeat("█", 1+p.Count*18/max(1, maxN))
		fmt.Fprintf(&b, " [white::b]%4d[-:-:-] [green]%s[-]\n      %s\n",
			p.Count, bar, tview.Escape(clip(p.Template, 110)))
	}
	return b.String()
}

// loadTrace fetches and renders the trace waterfall (on-demand, bounded).
func (a *App) loadTrace(traceID string) {
	a.trace.SetTitle(fmt.Sprintf(" Trace/%s ", traceID))
	a.trace.SetText("\n  [gray]reconstructing trace…").ScrollToBeginning()
	a.showPage("trace")
	go func() {
		start := time.Now()
		v, err := a.provider.Trace(context.Background(), traceID)
		slog.Debug("trace render", "id", traceID, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			if a.page != "trace" || a.detailRow.TraceID != traceID {
				return // navigated away
			}
			if err != nil {
				a.trace.SetText("\n  [red]✗ " + tview.Escape(err.Error()))
				return
			}
			a.trace.SetText(renderTrace(v))
		})
	}()
}

// renderTrace draws a span waterfall: each span indented by tree depth, with
// a proportional offset+duration bar, service:resource, and error marker.
func renderTrace(v *data.TraceView) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]trace %s[-:-:-]\n", tview.Escape(v.TraceID))
	fmt.Fprintf(&b, " [gray]%d spans · total %s · <l> logs for this trace · <esc> back[-]\n\n",
		len(v.Spans), data.FormatDuration(v.TotalUs))
	if len(v.Spans) == 0 {
		b.WriteString(" [gray]no spans found for this trace (retention/indexing, or wrong window)[-]\n")
		return b.String()
	}
	const barW, labelW = 40, 44
	for _, s := range v.Spans {
		indent := strings.Repeat("  ", s.Depth)
		// Compose the label from escaped user text + intentional color tags,
		// clipping the resource to fit; width is measured tag-free.
		label := tview.Escape(clip(indent+s.Service, labelW))
		if s.Resource != "" && visibleLen(label) < labelW-2 {
			room := labelW - visibleLen(label) - 1
			label += " [gray]" + tview.Escape(clip(s.Resource, room)) + "[-]"
		}
		bar := "[green]"
		if s.Error {
			bar = "[red]"
		}
		bar += traceBar(s.OffsetUs, s.DurationUs, v.TotalUs, barW) + "[-]"
		errTag := ""
		if s.Error {
			errTag = " [red]✗[-]"
		}
		fmt.Fprintf(&b, " %s%s %s [white]%s[-]%s\n",
			label, padVisible("", max(0, labelW-visibleLen(label))),
			bar, data.FormatDuration(s.DurationUs), errTag)
	}
	if v.Truncated {
		fmt.Fprintf(&b, "\n [yellow]trace truncated at %d spans[-]\n", 100)
	}
	return b.String()
}

// traceBar renders a span's position/length within the trace as leading
// spaces (offset) + block glyphs (duration), scaled to width w.
func traceBar(offsetUs, durUs, totalUs int64, w int) string {
	if totalUs <= 0 {
		return strings.Repeat("█", 1)
	}
	lead := int(offsetUs * int64(w) / totalUs)
	length := int(durUs * int64(w) / totalUs)
	if length < 1 {
		length = 1
	}
	if lead+length > w {
		lead = w - length
	}
	if lead < 0 {
		lead = 0
	}
	return strings.Repeat("·", lead) + strings.Repeat("█", length)
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
	a.resetView()
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
		page:         a.page,
		res:          a.res,
		filter:       a.filter,
		colFilterCol: a.colFilterCol,
		colFilterVal: a.colFilterVal,
		sortCol:      a.sortCol,
		sortAsc:      a.sortAsc,
		query:        a.queries[a.res.Key],
		detailRow:    a.detailRow,
		selRow:       sel,
	})
}

// resetView clears filters and sort — used when switching resource/context.
func (a *App) resetView() {
	a.filter = ""
	a.colFilterCol, a.colFilterVal = -1, ""
	a.sortCol, a.sortAsc = -1, true
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
	// k9s esc semantics: an active filter is cleared first; only a second
	// esc (nothing left to clear) pops the navigation stack.
	if a.page == "table" && (a.filter != "" || a.colFilterVal != "") {
		a.filter = ""
		a.colFilterCol, a.colFilterVal = -1, ""
		a.applyFilter()
		return
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
	a.colFilterCol, a.colFilterVal = e.colFilterCol, e.colFilterVal
	a.sortCol, a.sortAsc = e.sortCol, e.sortAsc
	a.detailRow = e.detailRow
	// Restore the server query too — a drill-down (monitor/trace → logs)
	// overwrote a.queries[res.Key], so popping back must put it back.
	if e.res.ServerQuery {
		a.queries[e.res.Key] = e.query
	}
	switch e.page {
	case "detail":
		a.renderDetail(e.detailRow)
		a.showPage("detail")
	case "dashboard":
		// The dashboard pane still holds its rendered text — just re-show
		// it (don't re-fetch and re-spend metric budget on a back-nav).
		a.showPage("dashboard")
	case "trace":
		a.showPage("trace") // pane still holds the rendered waterfall
	case "patterns":
		a.showPage("patterns") // pane still holds the rendered clusters
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
	case "dashboard":
		a.SetFocus(a.dash)
	case "trace":
		a.SetFocus(a.trace)
	case "patterns":
		a.SetFocus(a.patterns)
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
	a.showConfirm(
		fmt.Sprintf("Delete context %q?\nIts credentials are removed from the OS keychain;\nthe Datadog org itself is untouched.", name),
		[]string{"Cancel", "Delete"},
		func(label string) {
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
}

func (a *App) closeConfirm() {
	a.content.HidePage("confirm")
	a.page = "table"
	a.SetFocus(a.table)
	a.setHints()
}

// showConfirm displays a confirmation modal with the given buttons and calls
// onDone(label) with the chosen button after closing. A FRESH modal is built
// each time: a reused tview.Modal retains stale button focus across
// ClearButtons/AddButtons, which silently lands Enter on the wrong button.
func (a *App) showConfirm(text string, buttons []string, onDone func(label string)) {
	m := tview.NewModal().SetText(text).AddButtons(buttons)
	m.SetDoneFunc(func(_ int, label string) {
		a.closeConfirm()
		onDone(label)
	})
	a.confirm = m
	a.content.RemovePage("confirm").AddPage("confirm", m, true, false)
	a.page = "confirm"
	a.content.ShowPage("confirm")
	a.SetFocus(m)
	a.setHints()
}

// confirmIncidentAction offers to move the selected incident to another
// state. This is ike's only write path, so it is always behind this modal.
func (a *App) confirmIncidentAction(r data.Row) {
	cur := ""
	if len(r.Cells) > 2 {
		cur = strings.ToLower(r.Cells[2])
	}
	var targets []string
	for _, s := range data.IncidentStates {
		if s != cur {
			targets = append(targets, s)
		}
	}
	buttons := append([]string{"Cancel"}, targetLabels(targets)...)
	a.showConfirm(
		fmt.Sprintf("Change %s (currently %s) to:\nThis writes to Datadog.", r.ID, cur),
		buttons,
		func(label string) {
			state := strings.TrimPrefix(label, "→ ")
			if label == "Cancel" || state == "" {
				return
			}
			a.applyIncidentState(r, state)
		})
}

func targetLabels(states []string) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = "→ " + s
	}
	return out
}

func (a *App) applyIncidentState(r data.Row, state string) {
	a.flash("setting "+r.ID+" → "+state+" …", false)
	go func() {
		err := a.provider.SetIncidentState(context.Background(), r.ID, state)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("incident state change failed", "id", r.ID, "state", state, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash(r.ID+" → "+state, false)
			if a.res.Key == "incidents" && a.page == "table" {
				a.load(true) // cache was dropped; re-fetch to show the new state
			}
		})
	}()
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
	a.resetView()
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
	tr := a.timeRange()
	go func() {
		start := time.Now()
		rows, at, cached, err := a.provider.Fetch(context.Background(), res, q, tr, force)
		slog.Debug("fetch", "resource", res.Key, "query", q, "range", tr, "force", force,
			"rows", len(rows), "cached", cached, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			a.loading = false
			if a.res.Key != res.Key {
				return // user switched view while fetching
			}
			if err != nil {
				a.flash("✗ "+err.Error(), true)
				// A 429 means the org's shared budget is exhausted — stop the
				// auto-refresh timer from making it worse.
				if data.ErrorIsRateLimit(err) && !a.paused {
					a.paused = true
					a.setHints()
					slog.Warn("auto-refresh paused: rate limited")
				}
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
		if matchRow(r, a.colFilterCol, a.colFilterVal, a.filter) {
			a.filtered = append(a.filtered, i)
		}
	}
	a.sortFiltered()
	a.render()
}

// matchRow applies both filters: an exact (case-insensitive) match on one
// column (col>=0), and a substring match across all cells.
func matchRow(r data.Row, col int, val, text string) bool {
	if val != "" && col >= 0 {
		if col >= len(r.Cells) || !strings.EqualFold(r.Cells[col], val) {
			return false
		}
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

// sortFiltered orders a.filtered by the chosen column, falling back to the
// resource's natural order (already applied by the provider) when sortCol<0.
// Numeric-looking columns sort numerically; everything else case-insensitive.
func (a *App) sortFiltered() {
	if a.sortCol < 0 {
		return
	}
	col := a.sortCol
	sort.SliceStable(a.filtered, func(i, j int) bool {
		ci := cellAt(a.rows[a.filtered[i]], col)
		cj := cellAt(a.rows[a.filtered[j]], col)
		var less bool
		if ni, oki := parseNum(ci); oki {
			if nj, okj := parseNum(cj); okj {
				less = ni < nj
			} else {
				less = ci < cj
			}
		} else {
			less = strings.ToLower(ci) < strings.ToLower(cj)
		}
		if a.sortAsc {
			return less
		}
		return !less
	})
}

func cellAt(r data.Row, col int) string {
	if col < len(r.Cells) {
		return r.Cells[col]
	}
	return ""
}

// parseNum pulls a leading number out of a cell like "99.90%", "P1", "42".
func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] == '-' || s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	return f, err == nil
}

func (a *App) ticker() {
	if a.refreshEvery <= 0 {
		return // auto-refresh disabled entirely (refresh interval 0)
	}
	t := time.NewTicker(a.refreshEvery)
	defer t.Stop()
	for range t.C {
		if a.res.AutoRefresh && !a.loading && !a.paused {
			a.QueueUpdateDraw(func() { a.load(false) })
		} else {
			a.QueueUpdateDraw(a.updateInfo) // keep the Age counter moving
		}
	}
}

// toggleAutoRefresh pauses/resumes timer-driven refresh (ctrl-r still works).
func (a *App) toggleAutoRefresh() {
	a.paused = !a.paused
	if a.paused {
		a.flash("auto-refresh paused (ctrl-r to refresh manually, p to resume)", false)
	} else {
		a.flash("auto-refresh resumed", false)
	}
	a.setHints() // the hint line shows auto:on/off
	a.updateInfo()
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
	a.table.SetTitle(tview.Escape(fmt.Sprintf(" %s(%s)[%d]%s ", a.res.Title, flabel, len(a.filtered), sortLabel)))
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
		" [orange]Mode:[%s]   %s\n [orange]Site:[white]   %s\n [orange]View:[white]   %s\n [orange]Age:[white]    %s\n [orange]Budget:[white] %s",
		modeColor, mode, a.provider.Site(), view, age, budget))
}

// budgetLine matches the provider's "name remaining/limit per Ns" budget
// strings so the header can render compact, colour-coded headroom.
var budgetLine = regexp.MustCompile(`^(\S+)\s+(\d+)/(\d+)\s+per`)

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
	if a.res.Key == "dashboards" {
		a.pushNav()
		a.detailRow = r
		a.loadDashboard(r, false) // render widgets + sparklines instead of JSON
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
		b, err := json.MarshalIndent(full, "", "  ")
		if err != nil {
			return
		}
		body := string(b)
		// Monitors: prepend the evaluated metric as a sparkline — the data
		// behind the alert, so the detail answers "why is it firing?".
		if res.Key == "monitors" {
			if ms, mErr := a.provider.MonitorMetric(context.Background(), r.ID); mErr == nil {
				body = monitorMetricHeader(ms) + body
			}
		}
		a.QueueUpdateDraw(func() {
			if a.page != "detail" || a.detailRow.ID != r.ID {
				return // user navigated away meanwhile
			}
			row, col := a.detail.GetScrollOffset()
			a.detail.SetText(body)
			a.detail.ScrollTo(row, col)
			a.flash("full object loaded", false)
		})
	}()
}

// monitorMetricHeader renders the metric sparkline block shown above a
// monitor's JSON. The detail view has dynamic colours OFF (so raw JSON
// renders safely), hence this is plain text — no colour tags.
func monitorMetricHeader(ms *data.MetricSeries) string {
	var b strings.Builder
	b.WriteString("── metric (last 1h) ──\n")
	if ms.Query != "" {
		b.WriteString(ms.Query + "\n")
	}
	if len(ms.Points) > 0 {
		fmt.Fprintf(&b, "%s  last %s\n", data.Sparkline(ms.Points), data.FormatValue(ms.Last))
	} else if ms.Note != "" {
		b.WriteString(ms.Note + "\n")
	}
	b.WriteString("──────────────────────\n\n")
	return b.String()
}

func (a *App) renderDetail(r data.Row) {
	b, err := json.MarshalIndent(r.Raw, "", "  ")
	if err != nil {
		b = []byte("✗ " + err.Error())
	}
	a.detail.SetText(string(b)).ScrollToBeginning()
	a.detail.SetTitle(fmt.Sprintf(" %s/%s ", strings.TrimSuffix(a.res.Title, "s"), r.ID))
}

// loadDashboard renders a dashboard's widgets with sparklines. Fetch is
// on-demand and bounded (see data.maxDashWidgets); force=true is ctrl-r.
func (a *App) loadDashboard(r data.Row, force bool) {
	a.dash.SetTitle(fmt.Sprintf(" Dashboard/%s ", r.ID))
	if !force {
		a.dash.SetText("\n  [gray]loading widgets…").ScrollToBeginning()
	} else {
		a.flash("refreshing sparklines…", false)
	}
	a.showPage("dashboard")
	go func() {
		start := time.Now()
		view, err := a.provider.Dashboard(context.Background(), r.ID)
		slog.Debug("dashboard render", "id", r.ID, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			if a.page != "dashboard" || a.detailRow.ID != r.ID {
				return // navigated away
			}
			if err != nil {
				a.dash.SetText("\n  [red]✗ " + tview.Escape(err.Error()))
				return
			}
			a.dash.SetText(renderDashboard(view))
			if force {
				a.flash("sparklines refreshed", false)
			}
		})
	}()
}

// dashGridCols is Datadog's dashboard grid width in layout units; widgets
// pack into terminal rows until their widths sum past it, approximating the
// real 2-D arrangement (a width-12 widget fills a row; two width-6 share one).
const dashGridCols = 12

// renderDashboard turns a DashboardView into the terminal panel. When the
// dashboard has layout coordinates it renders a grid (widgets side by side,
// in Datadog reading order); otherwise it falls back to a one-per-line list.
func renderDashboard(v *data.DashboardView) string {
	var b strings.Builder
	fmt.Fprintf(&b, " [orange::b]%s[-:-:-]\n", tview.Escape(v.Title))
	fmt.Fprintf(&b, " [gray]%d widgets · sparklines cover the last 1h · <ctrl-r> to refresh[-]\n\n", len(v.Widgets))

	hasCoords := false
	for _, w := range v.Widgets {
		if w.W > 0 {
			hasCoords = true
			break
		}
	}

	if !hasCoords {
		for _, w := range v.Widgets {
			b.WriteString(widgetLines(w, 0))
			b.WriteString("\n")
		}
	} else {
		ws := make([]data.Widget, len(v.Widgets))
		copy(ws, v.Widgets)
		sort.SliceStable(ws, func(i, j int) bool {
			if ws[i].Y != ws[j].Y {
				return ws[i].Y < ws[j].Y
			}
			return ws[i].X < ws[j].X
		})
		// Greedily pack widgets into rows by the 12-unit grid width.
		var row []data.Widget
		units := 0
		flush := func() {
			if len(row) > 0 {
				b.WriteString(renderWidgetRow(row))
				row, units = nil, 0
			}
		}
		for _, w := range ws {
			wu := w.W
			if wu <= 0 {
				wu = dashGridCols
			}
			if units+wu > dashGridCols {
				flush()
			}
			row = append(row, w)
			units += wu
		}
		flush()
	}

	if v.Truncated {
		fmt.Fprintf(&b, " [yellow]Note: only the first %d metric widgets were charted to protect the API budget.[-]\n", data.MaxDashWidgets)
	}
	return b.String()
}

// renderWidgetRow lays out a row of widgets side by side in equal-width
// terminal columns, zipping their lines together with tag-aware padding.
func renderWidgetRow(row []data.Widget) string {
	const rowWidth = 96
	cellW := rowWidth/len(row) - 2
	if cellW < 18 {
		cellW = 18
	}
	cells := make([][]string, len(row))
	maxLines := 0
	for i, w := range row {
		cells[i] = strings.Split(strings.TrimRight(widgetLines(w, cellW), "\n"), "\n")
		if len(cells[i]) > maxLines {
			maxLines = len(cells[i])
		}
	}
	var b strings.Builder
	for ln := 0; ln < maxLines; ln++ {
		b.WriteString(" ")
		for i := range cells {
			cell := ""
			if ln < len(cells[i]) {
				cell = cells[i][ln]
			}
			b.WriteString(padVisible(cell, cellW))
			b.WriteString("  ")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// widgetLines renders one widget as title / sparkline+value (or note) /
// query. width>0 truncates the sparkline and query to fit a grid cell.
func widgetLines(w data.Widget, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[aqua::b]%s[-:-:-] [gray]%s[-]\n", tview.Escape(clip(w.Title, width)), tview.Escape(w.Type))
	switch {
	case w.HasData:
		spark := data.Sparkline(w.Spark)
		if width > 10 && len(spark) > width-10 {
			spark = spark[len(spark)-(width-10):] // keep the most recent points
		}
		fmt.Fprintf(&b, "[green]%s[-] [white::b]%s[-:-:-]\n", spark, data.FormatValue(w.Last))
	case w.Note != "":
		fmt.Fprintf(&b, "[gray]· %s[-]\n", tview.Escape(clip(w.Note, width)))
	}
	if w.Query != "" {
		fmt.Fprintf(&b, "[darkcyan]%s[-]\n", tview.Escape(clip(w.Query, width)))
	}
	return b.String()
}

var tagRe = regexp.MustCompile(`\[[a-zA-Z0-9_,:;.#-]*\]`)

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

// copyClipboard writes s to the OS clipboard via the platform tool (pbcopy
// on macOS; wl-copy or xclip on Linux). The value is piped on stdin, never
// passed as an argument, so it can't leak via the process list.
func copyClipboard(s string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		}
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}
