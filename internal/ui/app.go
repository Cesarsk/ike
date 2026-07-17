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
	promptSaveQuery // naming the current query for the 'Q' picker
	promptSettings  // typing a TTL/columns value in the :settings editor
	promptTodo      // typing an incident to-do's content ('T')
)

// ContextInfo describes one selectable Datadog org context for the :ctx view.
type ContextInfo struct {
	Name string
	Site string
	Keys string // where the credentials come from, e.g. "$IKE_DEV_API_KEY"
}

// SavedQuery is a bookmarked, view-scoped query recalled via the 'Q' picker.
type SavedQuery struct {
	Name  string
	View  string
	Query string
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
	// TTLOverrides maps a resource key to a custom cache TTL from the config
	// file, overriding the built-in default (empty = use defaults).
	TTLOverrides map[string]time.Duration
	// Columns maps a resource key to the display column subset/order (by
	// name) from the config file (empty = show all columns in registry order).
	Columns map[string][]string
	// Theme names a built-in colour palette (empty/unknown = "default").
	Theme string
	// SavedQueries returns the bookmarked queries for a context (nil = none).
	// SaveQuery / DeleteQuery persist a change for a context; all three may be
	// nil to disable the feature (e.g. demo mode wires in-memory versions).
	SavedQueries func(context string) []SavedQuery
	SaveQuery    func(context, name, view, query string) error
	DeleteQuery  func(context, name, view string) error
	// SaveSettings persists the theme + per-view TTL overrides + columns edited
	// in :settings back to the config file (nil = don't persist, e.g. demo).
	SaveSettings func(theme string, ttl map[string]string, columns map[string][]string) error
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
	theme        Theme
	ctxInfos     []ContextInfo
	current      string // active context name
	refreshEvery time.Duration

	content   *tview.Pages // "table" | "detail" | "help" | "ctxform" (+ "confirm" overlay)
	infoTV    *tview.TextView
	hintTV    *tview.TextView
	table     *tview.Table
	prompt    *tview.InputField
	status    *tview.TextView
	footer    *tview.Pages
	detail    *tview.TextView
	dash      *tview.TextView
	trace     *tview.TextView
	patterns  *tview.TextView
	ctxForm   *tview.Form
	formErr   *tview.TextView
	confirm   *tview.Modal
	savedQL   *tview.List  // the 'Q' saved-query picker
	savedQV   string       // view the open picker is scoped to
	savedQIt  []SavedQuery // items backing the picker, by list index
	pendSaveQ string       // query pending a name (save prompt in flight)

	settingsTbl *tview.Table // the :settings editor
	settingRows []settingRow // editable settings, indexed by table data row
	editingSet  int          // settingRows index being edited (prompt in flight)

	todoIncidentID string // incident whose to-dos the panel shows / to-do add in flight

	colPick      *tview.List // the 'C' column picker
	colPickView  string      // view whose columns the picker is editing
	colPickItems []colItem   // picker rows (column + shown), in display order

	// userpick: the searchable commander/assignee picker ('I', to-do assign)
	userPickFlex    *tview.Flex
	userSearch      *tview.InputField
	userPick        *tview.List
	userPickItems   []data.User     // list rows, in display order
	userPickOnPick  func(data.User) // what to do with the chosen user
	userPickReturn  string          // page to return to on pick/cancel
	userPickSeq     int             // drops stale async search results
	userSearchTimer *time.Timer     // search debounce
	pickSelf        *data.User      // cached acting user, pinned atop an empty search
	pendTodoContent string          // to-do content awaiting an assignee pick

	// todos: the incident to-do panel ('T')
	todoList  *tview.List
	todoItems []data.Todo // list rows, in display order

	confirmReturn string // page to restore after a confirm modal closes

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
	ctrlCAt    time.Time // last ctrl-c press, for double-press-to-quit
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

// applyTheme (re)applies the current palette's structural colours to every
// long-lived widget. Called from build() and whenever the theme changes at
// runtime (via :settings), so a theme switch takes effect without a restart.
func (a *App) applyTheme() {
	sel := tcell.StyleDefault.Background(a.theme.SelectBg).Foreground(a.theme.SelectFg)
	a.table.SetBorderColor(a.theme.Border)
	a.table.SetTitleColor(a.theme.Title)
	a.table.SetSelectedStyle(sel)
	a.prompt.SetLabelColor(a.theme.Label)
	a.prompt.SetFieldBackgroundColor(a.theme.FieldBg)
	a.prompt.SetFieldTextColor(a.theme.FieldFg)
	for _, tv := range []*tview.TextView{a.detail, a.dash, a.trace, a.patterns} {
		tv.SetBorderColor(a.theme.Border)
		tv.SetTitleColor(a.theme.Title)
	}
	a.savedQL.SetBorderColor(a.theme.Border)
	a.savedQL.SetTitleColor(a.theme.Title)
	if a.colPick != nil {
		a.colPick.SetBorderColor(a.theme.Border)
		a.colPick.SetTitleColor(a.theme.Title)
	}
	if a.userPickFlex != nil {
		a.userPickFlex.SetBorderColor(a.theme.Border)
		a.userPickFlex.SetTitleColor(a.theme.Title)
		a.userSearch.SetLabelColor(a.theme.Label)
		a.userSearch.SetFieldBackgroundColor(a.theme.FieldBg)
		a.userSearch.SetFieldTextColor(a.theme.FieldFg)
		a.userPick.SetSelectedStyle(sel)
	}
	if a.todoList != nil {
		a.todoList.SetBorderColor(a.theme.Border)
		a.todoList.SetTitleColor(a.theme.Title)
		a.todoList.SetSelectedStyle(sel)
	}
	a.ctxForm.SetTitleColor(a.theme.Title)
	a.ctxForm.SetBorderColor(a.theme.Border)
	a.ctxForm.SetFieldBackgroundColor(a.theme.FieldBg)
	a.ctxForm.SetButtonBackgroundColor(a.theme.Button)
	a.ctxForm.SetLabelColor(a.theme.Label)
	if a.settingsTbl != nil {
		a.settingsTbl.SetBorderColor(a.theme.Border)
		a.settingsTbl.SetTitleColor(a.theme.Title)
		a.settingsTbl.SetSelectedStyle(sel)
	}
}

func (a *App) build() {
	a.theme = ResolveTheme(a.opts.Theme)
	a.infoTV = tview.NewTextView().SetDynamicColors(true)
	a.hintTV = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)

	header := tview.NewFlex().
		AddItem(a.infoTV, 0, 2, false).
		AddItem(a.hintTV, 0, 3, false)

	a.table = tview.NewTable().
		SetFixed(1, 0).
		SetSelectable(true, false)
	a.table.SetBorder(true)
	a.table.SetSelectedFunc(func(row, _ int) { a.openDetail(row) })

	a.prompt = tview.NewInputField()
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
			return commandCompletions(current)
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

	a.dash = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.dash.SetBorder(true)

	a.trace = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.trace.SetBorder(true)

	a.patterns = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.patterns.SetBorder(true)

	a.savedQL = tview.NewList().ShowSecondaryText(true)
	a.savedQL.SetBorder(true)
	a.savedQL.SetMainTextColor(tcell.ColorWhite)
	a.savedQL.SetSecondaryTextColor(tcell.ColorGray)
	a.savedQL.SetSelectedFunc(func(i int, _, _ string, _ rune) { a.applySavedQuery(i) })

	a.settingsTbl = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false)
	a.settingsTbl.SetBorder(true)
	a.settingsTbl.SetSelectedFunc(func(row, _ int) { a.editSetting(row) })

	a.colPick = tview.NewList().ShowSecondaryText(false)
	a.colPick.SetBorder(true)
	a.colPick.SetMainTextColor(tcell.ColorWhite)

	// userpick: a search field over a results list. Focus stays on the search
	// field (so typing filters); the keys handler routes ↑/↓/enter/esc to the
	// list. See userpick.go.
	a.userSearch = tview.NewInputField().SetLabel(" search users> ")
	a.userSearch.SetChangedFunc(func(string) { a.scheduleUserSearch() })
	a.userPick = tview.NewList().ShowSecondaryText(false)
	a.userPick.SetMainTextColor(tcell.ColorWhite)
	a.userPickFlex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.userSearch, 1, 0, true).
		AddItem(a.userPick, 0, 1, false)
	a.userPickFlex.SetBorder(true).SetTitle(" Assign ")

	// todos: the incident to-do panel. See todos.go.
	a.todoList = tview.NewList().ShowSecondaryText(true)
	a.todoList.SetBorder(true)
	a.todoList.SetMainTextColor(tcell.ColorWhite)
	a.todoList.SetSecondaryTextColor(tcell.ColorGray)

	a.ctxForm = tview.NewForm()
	a.ctxForm.SetBorder(true)
	a.ctxForm.SetTitle(" Add context ")

	a.confirm = tview.NewModal()

	// The add-context form sits beside a guidance panel explaining where
	// each credential comes from, with a validation-error line on top so
	// rejects are visible right where the user is looking (not only in the
	// status bar at the bottom).
	guidance := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	guidance.SetBorder(true).SetTitle(" Guidance ").SetTitleColor(a.theme.Title)
	guidance.SetBorderColor(a.theme.Border)
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
		AddPage("savedq", a.savedQL, true, false).
		AddPage("settings", a.settingsTbl, true, false).
		AddPage("colpick", a.colPick, true, false).
		AddPage("userpick", a.userPickFlex, true, false).
		AddPage("todos", a.todoList, true, false).
		AddPage("help", a.buildHelp(), true, false).
		AddPage("ctxform", ctxFormFlex, true, false).
		AddPage("confirm", a.confirm, true, false)
	a.page = "table"

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(a.content, 0, 1, true).
		AddItem(a.footer, 1, 0, false)

	a.applyTheme() // single-source the palette onto every widget
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
	case "userpick":
		lines = []string{
			"[aqua]<type>[white]search users (from Datadog)  [aqua]<↑/↓>[white]select",
			"[aqua]<enter>[white]choose  [aqua]<esc>[white]cancel",
		}
	case "todos":
		lines = []string{
			"[aqua]<a>[white]add  [aqua]<c/space/enter>[white]toggle done  [aqua]<d>[white]delete",
			"[aqua]<↑/↓ j/k>[white]move  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	default:
		refresh := "on"
		if a.paused {
			refresh = "off"
		}
		lines = []string{
			"[aqua]<:>[white]cmd  [aqua]</>[white]filter  [aqua]<enter>[white]details  [aqua]<o>[white]open  [aqua]<c>[white]copy  [aqua]<C>[white]cols",
			fmt.Sprintf("[aqua]<ctrl-r>[white]refresh  [aqua]<p>[white]auto:%s  [aqua]<esc>[white]back  [aqua]<?>[white]help  [aqua]<q>[white]quit", refresh),
			"",
			"[orange]:monitors :incidents :slos :logs :traces :services :events :downtimes :dashboards :ctx :settings",
		}
		switch a.res.Key {
		case "monitors":
			lines = append(lines, "[gray]<l>logs  <m>mute  <s>sort <S>rev   quick: <1>alert <2>warn <3>nodata <4>ok <0>all")
		case "slos":
			lines = append(lines, "[gray]<enter>error budget  <t>cycle type filter  <s>sort <S>reverse")
		case "incidents":
			lines = append(lines, "[gray]<r>state  <v>severity  <I>commander (pick)  <T>to-dos  quick: <1>active <2>stable <3>resolved <0>all")
		case "downtimes":
			lines = append(lines, "[gray]<x>cancel downtime  <s>sort <S>reverse")
		case "logs":
			lines = append(lines, "[gray]</>query (tab=complete, ↑ history)  <t>trace  <P>patterns  <Q>saved  window: <1>15m..<5>7d")
		case "traces":
			lines = append(lines, "[gray]</>query  <t>trace waterfall  <l>logs for trace  <Q>saved  window: <1>15m..<5>7d")
		case "services":
			lines = append(lines, "[gray]<enter>traces for service  </>env (default prod)  <s>sort <S>reverse")
		case "events":
			lines = append(lines, "[gray]</>query  <Q>saved  window: <1>15m..<5>7d  <s>sort   (deploys, alerts, changes)")
		case ctxResource.Key:
			lines = append(lines, "[gray]<enter>switch org  <a>add  <e>edit config  <ctrl-d>delete")
		default:
			lines = append(lines, "[gray]<s>sort <S>reverse")
		}
	}
	a.hintTV.SetText(a.theme.recolor(strings.Join(lines, "\n")))
}

func (a *App) buildHelp() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Help ").SetTitleColor(a.theme.Title)
	fmt.Fprint(tv, a.theme.recolor(`
 [orange]NAVIGATION
   [aqua]:<resource>[white]   switch view: monitors incidents slos logs traces services
                 events downtimes dashboards (aliases: mon inc s l tr svc ev dt d) — :ctx / :settings
   [aqua]enter[white]         detail — full object on demand; SLO error budget; monitor metric
                 sparkline; on a dashboard its widget grid; on logs/traces a row
   [aqua]esc[white]           go back (navigation history, k9s-style); clears the active filter
   [aqua]↑/↓ j/k[white]       move selection / scroll (↑/↓ in the / prompt = query history)
   [aqua]o[white]             open the selected item in the Datadog web UI (works in detail too)

 [orange]SEARCH, SORT & FILTER
   [aqua]/<text>[white]       filter rows; in Logs/Traces/Events it is a Datadog query (server-side,
                 tab-completes facets/values; ↑ recalls previous queries)
   [aqua]s / S[white]         cycle the sort column / reverse the direction (any table)
   [aqua]0-4[white]           quick filter by status — monitors: alert/warn/nodata/ok/all;
                 incidents: active/stable/resolved/all
   [aqua]1-5[white]           (logs/traces/events) time window: 15m / 1h / 4h / 1d / 7d
   [aqua]t[white]             (SLOs) cycle the Type filter: metric / monitor / time_slice / all
   [aqua]P[white]             (logs) cluster the loaded lines into patterns — flood triage
   [aqua]Q[white]             (logs/traces/events) saved-query picker — [aqua]enter[white] apply, [aqua]a[white] save, [aqua]d[white] delete

 [orange]CORRELATION (the debugging loop)
   [aqua]enter[white]         (service) → its traces (service:<name>) — services ▸ traces ▸ logs
   [aqua]l[white]             drill to logs — (monitor) its log query; (trace) that trace's logs
   [aqua]t[white]             drill to the trace waterfall — (logs/traces) the row's trace_id;
                 needs APM log-injection, else a clear "no trace_id"

 [orange]ACTIONS
   [aqua]m[white]             (monitor) mute / unmute — behind a confirmation
   [aqua]r[white]             (incident) change state (active/stable/resolved) — behind a confirm
   [aqua]v[white]             (incident) change severity (SEV-1…SEV-5) — behind a confirm
   [aqua]I[white]             (incident) assign commander — searchable user picker (you pinned
                 on top: enter = take command), behind a confirm
   [aqua]T[white]             (incident) to-do panel — list / add (assign to anyone) / toggle
                 done / delete action items
   [gray]              (incident) commander & responders show in the detail (enter) — responders
                 are read-only; the API has no write path for them
   [aqua]x[white]             (downtime) cancel the selected downtime — behind a confirm
   [aqua]c[white]             copy the row's URL / query / id to the clipboard
   [aqua]ctrl-r[white]        force refresh (bypasses cache — spends API budget)
   [aqua]p[white]             pause / resume auto-refresh (header shows auto:on/off)

 [orange]CONTEXTS (:ctx)
   [aqua]enter[white]         switch org (cache, budget and history are dropped — a hard boundary)
   [aqua]a[white]             add a context (name, site, API/APP keys or access token → OS keychain)
   [aqua]e[white]             edit the config file in $EDITOR, then reload + re-validate
   [aqua]ctrl-d[white]        delete the selected context (asks first)

 [orange]OTHER
   [aqua]C[white]             (any table) column picker — [aqua]space[white] show/hide, [aqua]J/K[white] reorder; live + saved
   [aqua]:settings[white]     theme and per-view cache TTLs — applies live + saved to config
   [aqua]?[white]             this help (from any view)
   [aqua]q[white]             back in detail/help; quit from a table view
   [aqua]ctrl-c[white]        quit — press twice to confirm (also :q :quit :exit)

 [gray]Views auto-refresh only where cheap (monitors, incidents), else cached per TTL.
 [gray]The Budget header shows Datadog X-RateLimit headroom; a 429 auto-pauses refresh.
`))
	return tv
}

// ---- input ----------------------------------------------------------------

func (a *App) keys(ev *tcell.EventKey) *tcell.EventKey {
	// ctrl-c anywhere: quit, but require a second press within 2s so it's a
	// deliberate exit (tview would otherwise quit on the first press). 'c'
	// stays copy; this is the only ctrl-c path.
	if ev.Key() == tcell.KeyCtrlC {
		if time.Since(a.ctrlCAt) < 2*time.Second {
			a.Stop()
			return nil
		}
		a.ctrlCAt = time.Now()
		a.flash("press ctrl-c again to quit", false)
		return nil
	}
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
	case "savedq":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == 'a':
			a.saveCurrentQuery()
			return nil
		case ev.Rune() == 'd':
			a.deleteSelectedQuery()
			return nil
		case ev.Rune() == ':':
			a.openPrompt(promptCmd)
			return nil
		case ev.Rune() == '?':
			a.showHelp()
			return nil
		}
		return ev // the List handles ↑/↓ and enter (apply)
	case "settings":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == ':':
			a.openPrompt(promptCmd)
			return nil
		case ev.Rune() == '?':
			a.showHelp()
			return nil
		}
		return ev // the table handles ↑/↓ and enter (edit)
	case "colpick":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back() // edits already persisted on each change
			return nil
		case ev.Rune() == ' ':
			a.toggleColumn()
			return nil
		case ev.Rune() == 'J':
			a.moveColumn(1)
			return nil
		case ev.Rune() == 'K':
			a.moveColumn(-1)
			return nil
		case ev.Rune() == ':':
			a.openPrompt(promptCmd)
			return nil
		case ev.Rune() == '?':
			a.showHelp()
			return nil
		}
		return ev // the list handles ↑/↓ j/k navigation
	case "userpick":
		// A text-entry modal: esc/enter/arrows are actions; every other key
		// types into the search field (so ':' etc. are searchable, not commands).
		switch {
		case ev.Key() == tcell.KeyEscape:
			a.closeUserPick()
			return nil
		case ev.Key() == tcell.KeyEnter:
			a.userPickChoose()
			return nil
		case ev.Key() == tcell.KeyUp:
			a.userPickMove(-1)
			return nil
		case ev.Key() == tcell.KeyDown:
			a.userPickMove(1)
			return nil
		}
		return ev // everything else types into the search field
	case "todos":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == 'a':
			a.addTodoFlow()
			return nil
		case ev.Rune() == 'c' || ev.Rune() == ' ' || ev.Key() == tcell.KeyEnter:
			a.toggleTodoComplete()
			return nil
		case ev.Rune() == 'd':
			a.deleteTodoFlow()
			return nil
		case ev.Rune() == ':':
			a.openPrompt(promptCmd)
			return nil
		case ev.Rune() == '?':
			a.showHelp()
			return nil
		}
		return ev // the list handles ↑/↓ j/k navigation
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
	case 'v':
		if a.res.Key == "incidents" {
			if row, ok := a.selectedRow(); ok {
				a.confirmIncidentSeverity(row)
			}
			return nil
		}
	case 'I':
		if a.res.Key == "incidents" {
			if row, ok := a.selectedRow(); ok {
				a.assignCommanderFlow(row)
			}
			return nil
		}
	case 'T':
		if a.res.Key == "incidents" {
			if row, ok := a.selectedRow(); ok {
				a.openTodoPanel(row.ID)
			}
			return nil
		}
	case 'x':
		if a.res.Key == "downtimes" {
			if row, ok := a.selectedRow(); ok {
				a.confirmCancelDowntime(row)
			}
			return nil
		}
	case 'Q':
		if a.res.ServerQuery && a.opts.SavedQueries != nil {
			a.openSavedQueries()
			return nil
		}
	case 'C':
		if a.res.Key != ctxResource.Key {
			a.openColumnPicker() // no-op if the view has no columns
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
	switch {
	case m == promptCmd:
		a.prompt.SetLabel(" 🐶 > ")
	case m == promptSaveQuery:
		a.prompt.SetLabel(" save query as> ") // empty field: type a name
	case m == promptSettings:
		if a.editingSet >= 0 && a.editingSet < len(a.settingRows) {
			s := a.settingRows[a.editingSet]
			a.prompt.SetLabel(" " + s.label + "> ")
			prefill = a.settingRawValue(s)
		}
	case m == promptTodo:
		a.prompt.SetLabel(" to-do for " + a.todoIncidentID + "> ")
	case a.res.ServerQuery:
		a.prompt.SetLabel(" query> ")
		prefill = a.queries[a.res.Key] // edit the current query, don't retype
		if prefill == "*" {
			prefill = ""
		}
	default:
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
	switch a.page {
	case "detail":
		a.SetFocus(a.detail)
	case "savedq":
		a.SetFocus(a.savedQL)
	case "settings":
		a.SetFocus(a.settingsTbl)
	case "todos":
		a.SetFocus(a.todoList)
	default:
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
	case promptSaveQuery:
		if text == "" || a.opts.SaveQuery == nil {
			return
		}
		if err := a.opts.SaveQuery(a.current, text, a.savedQV, a.pendSaveQ); err != nil {
			a.flash("✗ "+err.Error(), true)
			return
		}
		a.flash("saved "+text, false)
		if a.page == "savedq" {
			a.refreshSavedQueries()
		}
	case promptSettings:
		a.applySettingInput(text)
	case promptTodo:
		if text == "" || a.todoIncidentID == "" {
			return
		}
		inc := a.todoIncidentID
		a.pendTodoContent = text
		// Pick the assignee (self pinned on top) before creating the to-do.
		a.openUserPicker("Assign to-do · "+inc, func(u data.User) {
			a.addTodoAssigned(inc, a.pendTodoContent, u.Handle)
		})
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
	if cmd == "settings" || cmd == "set" || cmd == "config" {
		a.showSettings()
		return
	}
	if res, ok := data.ResourceByAlias(cmd); ok {
		a.switchResource(res)
		return
	}
	a.flash(fmt.Sprintf("unknown command %q — try :monitors :incidents :slos :logs :dashboards :ctx :settings", cmd), true)
}

// commandCompletions returns the ':' command names offered by autocomplete —
// every resource key plus the pseudo-commands execCommand accepts. Kept next to
// execCommand so the two don't drift (the earlier ':' gap was exactly that).
func commandCompletions(prefix string) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	names := make([]string, 0, len(data.Resources())+4)
	for _, r := range data.Resources() {
		names = append(names, r.Key)
	}
	names = append(names, "ctx", "settings", "help", "quit")
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
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

// drillToServiceTraces is enter on a Services row: open the traces view scoped
// to that service (service:<name>) — the services → traces → logs loop.
func (a *App) drillToServiceTraces(r data.Row) {
	tr, ok := data.ResourceByAlias("traces")
	if !ok {
		return
	}
	a.queries["traces"] = "service:" + r.ID
	a.switchResource(tr) // pushes nav, so esc returns to :services
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

// openSavedQueries shows the 'Q' picker for the current view's bookmarked
// queries: enter applies, 'a' saves the active query, 'd' deletes, esc backs.
func (a *App) openSavedQueries() {
	a.savedQV = a.res.Key
	a.pushNav()
	a.refreshSavedQueries()
	a.showPage("savedq")
}

// refreshSavedQueries repopulates the picker from the current context, scoped
// to the picker's view. savedQIt tracks the real entries by list index so the
// trailing placeholder (shown when empty) is never applied or deleted.
func (a *App) refreshSavedQueries() {
	a.savedQL.Clear()
	a.savedQIt = nil
	if a.opts.SavedQueries != nil {
		for _, q := range a.opts.SavedQueries(a.current) {
			if q.View != a.savedQV {
				continue
			}
			a.savedQIt = append(a.savedQIt, q)
			a.savedQL.AddItem(q.Name, q.Query, 0, nil)
		}
	}
	a.savedQL.SetTitle(fmt.Sprintf(" Saved queries · %s [%d]  <enter>apply <a>save <d>delete ", a.savedQV, len(a.savedQIt)))
	if len(a.savedQIt) == 0 {
		a.savedQL.AddItem("(none yet)", "press <a> to save the current query", 0, nil)
	}
}

// applySavedQuery sets the selected query as the view's query, returns to the
// table and refetches. Index beyond the real entries = the placeholder.
func (a *App) applySavedQuery(i int) {
	if i < 0 || i >= len(a.savedQIt) {
		return
	}
	q := a.savedQIt[i]
	a.back() // pop the picker → the view's table
	a.queries[q.View] = q.Query
	a.flash("query: "+q.Name, false)
	a.load(true)
}

// saveCurrentQuery bookmarks the view's active query under a name typed at the
// prompt. No-op if there's nothing meaningful to save.
func (a *App) saveCurrentQuery() {
	if a.opts.SaveQuery == nil {
		return
	}
	q := a.queries[a.savedQV]
	if q == "" || q == "*" {
		a.flash("no query to save — type one with / first", true)
		return
	}
	a.pendSaveQ = q
	a.openPrompt(promptSaveQuery)
}

// deleteSelectedQuery removes the highlighted saved query and refreshes.
func (a *App) deleteSelectedQuery() {
	if a.opts.DeleteQuery == nil {
		return
	}
	i := a.savedQL.GetCurrentItem()
	if i < 0 || i >= len(a.savedQIt) {
		return
	}
	q := a.savedQIt[i]
	if err := a.opts.DeleteQuery(a.current, q.Name, q.View); err != nil {
		a.flash("✗ "+err.Error(), true)
		return
	}
	a.flash("deleted "+q.Name, false)
	a.refreshSavedQueries()
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

	// Unified request timeline: every service's logs for this trace, oldest
	// first — read the request story top-to-bottom across services.
	b.WriteString("\n [orange::b]logs in this trace (chronological, all services)[-:-:-]\n")
	if len(v.Logs) == 0 {
		b.WriteString(" [gray]no correlated logs — logs in this window may not carry trace_id[-]\n")
	}
	for _, lg := range v.Logs {
		statusColor := "[gray]"
		switch strings.ToLower(lg.Status) {
		case "error", "critical", "emergency":
			statusColor = "[red]"
		case "warn", "warning":
			statusColor = "[yellow]"
		case "info":
			statusColor = "[green]"
		}
		fmt.Fprintf(&b, " [gray]%s[-] %s%-5s[-] [aqua]%s[-] %s\n",
			lg.Time.Local().Format("15:04:05.000"), statusColor, clip(lg.Status, 5),
			tview.Escape(clip(lg.Service, 20)), tview.Escape(clip(lg.Message, 80)))
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
	case "savedq":
		a.SetFocus(a.savedQL)
	case "settings":
		a.SetFocus(a.settingsTbl)
	case "colpick":
		a.SetFocus(a.colPick)
	case "userpick":
		a.SetFocus(a.userSearch) // typing filters; keys handler routes ↑/↓/enter
	case "todos":
		a.SetFocus(a.todoList)
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
	ret := a.confirmReturn
	if ret == "" {
		ret = "table"
	}
	a.showPage(ret) // restore the page the modal was opened over (e.g. the to-do panel)
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
	a.confirmReturn = a.page // restore this page (not always "table") when the modal closes
	a.content.RemovePage("confirm").AddPage("confirm", m, true, false)
	a.page = "confirm"
	a.content.ShowPage("confirm")
	a.SetFocus(m)
	a.setHints()
}

// confirmIncidentAction offers to move the selected incident to another
// state, behind a confirmation modal (a write path).
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
		fmt.Sprintf("Change %s state (currently %s) to:\nThis writes to Datadog.", r.ID, cur),
		buttons,
		func(label string) {
			state := strings.TrimPrefix(label, "→ ")
			if label == "Cancel" || state == "" {
				return
			}
			a.applyIncidentField(r, "state", state, r.ID+" → "+state)
		})
}

// confirmIncidentSeverity offers to change the selected incident's severity,
// behind a confirmation modal (a write path).
func (a *App) confirmIncidentSeverity(r data.Row) {
	cur := ""
	if len(r.Cells) > 1 {
		cur = strings.ToUpper(r.Cells[1])
	}
	var targets []string
	for _, s := range data.IncidentSeverities {
		if s != cur {
			targets = append(targets, s)
		}
	}
	buttons := append([]string{"Cancel"}, targetLabels(targets)...)
	a.showConfirm(
		fmt.Sprintf("Change %s severity (currently %s) to:\nThis writes to Datadog.", r.ID, cur),
		buttons,
		func(label string) {
			sev := strings.TrimPrefix(label, "→ ")
			if label == "Cancel" || sev == "" {
				return
			}
			a.applyIncidentField(r, "severity", sev, r.ID+" → "+sev)
		})
}

func targetLabels(vals []string) []string {
	out := make([]string, len(vals))
	for i, s := range vals {
		out[i] = "→ " + s
	}
	return out
}

// applyIncidentField performs a confirmed incident write (state or severity)
// off the UI thread; ok is the success flash message.
func (a *App) applyIncidentField(r data.Row, field, value, ok string) {
	a.flash("setting "+r.ID+" "+field+" → "+value+" …", false)
	go func() {
		err := a.provider.SetIncidentField(context.Background(), r.ID, field, value)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("incident field change failed", "id", r.ID, "field", field, "value", value, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash(ok, false)
			if a.res.Key == "incidents" && a.page == "table" {
				a.load(true) // cache was dropped; re-fetch to show the change
			}
		})
	}()
}

// assignCommanderFlow opens the user picker (current user pinned on top, so
// Enter still means "take command") and, on a pick, confirms before writing.
func (a *App) assignCommanderFlow(r data.Row) {
	a.openUserPicker("Commander · "+r.ID, func(u data.User) {
		a.showConfirm(
			fmt.Sprintf("Assign %s commander to %s?\nThis writes to Datadog.", r.ID, u.Handle),
			[]string{"Cancel", "Assign"},
			func(label string) {
				if label != "Assign" {
					return
				}
				a.applyAssignCommander(r, u)
			})
	})
}

func (a *App) applyAssignCommander(r data.Row, u data.User) {
	a.flash("assigning "+r.ID+" commander …", false)
	go func() {
		err := a.provider.SetIncidentCommander(context.Background(), r.ID, u.ID)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("assign commander failed", "id", r.ID, "user", u.ID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			// No commander column in the list, so nothing to reload — the
			// cache drop (Cached) keeps the detail view fresh; leave the flash.
			a.flash(r.ID+" commander → "+u.Handle, false)
		})
	}()
}

// addTodoAssigned creates an incident to-do with the picked assignee handle,
// refreshing the panel if it's still open on the same incident.
func (a *App) addTodoAssigned(incidentID, content, handle string) {
	a.flash("adding to-do to "+incidentID+" …", false)
	go func() {
		err := a.provider.AddIncidentTodo(context.Background(), incidentID, content, handle)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("add to-do failed", "id", incidentID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash("to-do added → @"+handle, false)
			if a.page == "todos" && a.todoIncidentID == incidentID {
				a.refreshTodos()
			}
		})
	}()
}

// confirmCancelDowntime offers to cancel the selected downtime, behind a
// confirmation modal (a write path).
func (a *App) confirmCancelDowntime(r data.Row) {
	scope := ""
	if len(r.Cells) > 1 {
		scope = r.Cells[1]
	}
	a.showConfirm(
		fmt.Sprintf("Cancel downtime %s (scope %s)?\nThis writes to Datadog.", r.ID, scope),
		[]string{"Cancel", "Cancel downtime"},
		func(label string) {
			if label != "Cancel downtime" {
				return
			}
			a.applyCancelDowntime(r)
		})
}

func (a *App) applyCancelDowntime(r data.Row) {
	a.flash("cancelling downtime "+r.ID+" …", false)
	go func() {
		err := a.provider.CancelDowntime(context.Background(), r.ID)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("downtime cancel failed", "id", r.ID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash(r.ID+" canceled", false)
			if a.res.Key == "downtimes" && a.page == "table" {
				a.load(true) // cache was dropped; re-fetch to show the change
			}
		})
	}()
}

// ---- data flow -------------------------------------------------------------

// tuneResource applies config-driven customization to a resource as it enters
// the app — currently the per-resource cache TTL override from the config
// file. A single choke point (switchResource) so every view is covered.
func (a *App) tuneResource(res data.Resource) data.Resource {
	if d, ok := a.opts.TTLOverrides[res.Key]; ok {
		res.TTL = d
	}
	return res
}

// displayColumns returns the column names to render and their indices into a
// row's full Cells, honouring a config `columns` override for the current view.
func (a *App) displayColumns() (names []string, idx []int) {
	return projectColumns(a.res.Columns, a.opts.Columns[a.res.Key])
}

// projectColumns maps a desired column-name subset (want) onto the full
// registry column order, returning the display names and their indices into a
// row's Cells. Matching is case-insensitive; unknown names are skipped; an
// empty or all-unknown want yields the identity projection (all columns) so a
// typo can never blank the table. Display-only: row Cells stay in registry
// order, so sorting and filtering are unaffected.
func projectColumns(full, want []string) (names []string, idx []int) {
	if len(want) > 0 {
		pos := make(map[string]int, len(full))
		for i, c := range full {
			pos[strings.ToUpper(c)] = i
		}
		for _, w := range want {
			if i, ok := pos[strings.ToUpper(strings.TrimSpace(w))]; ok {
				names = append(names, full[i])
				idx = append(idx, i)
			}
		}
	}
	if len(names) == 0 {
		names = full
		idx = make([]int, len(full))
		for i := range full {
			idx[i] = i
		}
	}
	return names, idx
}

func (a *App) switchResource(res data.Resource) {
	if a.page == "table" && res.Key == a.res.Key {
		return // ':monitors' while on monitors — nothing to do
	}
	res = a.tuneResource(res)
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
	names, cidx := a.displayColumns()
	for c, col := range names {
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
		for c, ci := range cidx {
			val := ""
			if ci < len(r.Cells) {
				val = r.Cells[ci]
			}
			if len(val) > 200 {
				val = val[:197] + "…"
			}
			cell := tview.NewTableCell(tview.Escape(val)).
				SetTextColor(color).
				SetExpansion(expansion(names[c]))
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
	case "downtimes":
		switch strings.ToLower(r.Cells[0]) { // STATUS column
		case "active":
			return tcell.ColorYellow // something is currently muted — worth noticing
		case "scheduled":
			return tcell.ColorLightSkyBlue
		case "canceled", "ended":
			return tcell.ColorGray
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
	a.infoTV.SetText(a.theme.recolor(fmt.Sprintf(
		" [orange]Mode:[%s]   %s\n [orange]Site:[white]   %s\n [orange]View:[white]   %s\n [orange]Age:[white]    %s\n [orange]Budget:[white] %s",
		modeColor, mode, a.provider.Site(), view, age, budget)))
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
	if a.res.Key == "services" {
		a.drillToServiceTraces(r) // enter on a service → its traces (the loop)
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
		var body string
		switch res.Key {
		case "incidents":
			// A People header (resolved handles) over the raw incident object —
			// turning an opaque JSON dump into something readable at a glance.
			if d, ok := full.(*data.IncidentDetail); ok {
				body = incidentPeopleHeader(d.People) + jsonIndent(d.Incident)
			} else {
				body = jsonIndent(full)
			}
		case "monitors":
			// Prepend the evaluated metric as a sparkline — the data behind the
			// alert, so the detail answers "why is it firing?".
			body = jsonIndent(full)
			if ms, mErr := a.provider.MonitorMetric(context.Background(), r.ID); mErr == nil {
				body = monitorMetricHeader(ms) + body
			}
		default:
			body = jsonIndent(full)
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

// jsonIndent renders a value as indented JSON for the detail view (dynamic
// colours are OFF there, so raw JSON is safe), surfacing any marshal error.
func jsonIndent(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "✗ " + err.Error()
	}
	return string(b)
}

// incidentPeopleHeader renders the resolved People block shown above an
// incident's JSON. Plain text (no colour tags — the detail view has dynamic
// colours off). Responders are read-only (the API has no write path); an
// unresolved responder shows its raw id.
func incidentPeopleHeader(p data.IncidentPeople) string {
	dash := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	responders := "—"
	if len(p.Responders) > 0 {
		responders = strings.Join(p.Responders, ", ")
	}
	var b strings.Builder
	b.WriteString("── people ──\n")
	fmt.Fprintf(&b, "  %-13s%s\n", "commander:", dash(p.Commander))
	fmt.Fprintf(&b, "  %-13s%s\n", "responders:", responders)
	fmt.Fprintf(&b, "  %-13s%s\n", "declared by:", dash(p.DeclaredBy))
	fmt.Fprintf(&b, "  %-13s%s\n", "created by:", dash(p.CreatedBy))
	b.WriteString("────────────\n\n")
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
