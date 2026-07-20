package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	// Auth is the context's auth shape: "oauth" (browser sign-in), "token"
	// (access token), or "" (API+APP key pair / env). It decides how 'O' and a
	// failed switch behave in :ctx.
	Auth string
	// Subdomain is the org's custom web subdomain (empty = app.<site>), shown
	// so the edit form can pre-fill it.
	Subdomain string
	// Active marks the context as explicitly activated for org-spanning views
	// (space in :ctx). The current context is always implicitly active.
	Active bool
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
	AddContext func(name, site, apiKey, appKey, token, subdomain string) (ContextInfo, error)
	// UpdateContext edits an existing context via the :ctx form ('e'). authMode
	// is "oauth" | "keys" | "token"; for keys/token, empty credential args mean
	// "keep the stored secret". nil = editing is unavailable (e.g. demo mode).
	UpdateContext func(name, authMode, site, apiKey, appKey, token, subdomain string) (ContextInfo, error)
	DeleteContext func(name string) error
	// ConfigPath is the contexts file (shown in :ctx); empty in demo mode.
	ConfigPath string
	Refresh    time.Duration
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
	// CurrentView is the resource view to reopen on (from config; empty/unknown
	// falls back to the first resource). Restored alongside the org.
	CurrentView string
	// PersistSession remembers the active org + view across sessions, called as
	// the user switches context or view (nil = don't persist, e.g. demo).
	PersistSession func(context, view string) error
	// PersistActive saves a context's explicit-activation flag (space in :ctx)
	// to the config (nil = in-session only, e.g. demo).
	PersistActive func(context string, active bool) error
	// AddOAuthContext creates a pending OAuth context from the :ctx add form
	// (auth = browser sign-in): the entry is persisted, but no tokens exist
	// until the user signs in with 'O'. nil = unavailable (e.g. demo mode).
	AddOAuthContext func(name, site, subdomain string) (ContextInfo, error)
	// OAuthLogin runs the browser sign-in for the named context and returns its
	// refreshed info once tokens are stored — site/subdomain come from the
	// stored context. On a key/token context it converts it to OAuth. Blocking
	// (the user completes the login in the browser), so the UI calls it off the
	// main thread. nil = the feature is unavailable (demo mode).
	OAuthLogin func(name string) (ContextInfo, error)
	// Version is shown on the startup splash (goreleaser ldflag; "dev" locally).
	Version string
	// FirstRun opens the getting-started page once at launch (reopen any time
	// with :manual). MarkIntroSeen persists that it was shown, so the next
	// session starts normally; nil = don't persist (demo mode never sets FirstRun).
	FirstRun      bool
	MarkIntroSeen func() error
}

// ctxResource is the :ctx pseudo-resource. It is rendered like any table but
// served from the app's own context list, never from a Provider, and enter
// switches context instead of opening a detail view.
var ctxResource = data.Resource{
	Key:     "contexts",
	Title:   "Contexts",
	Columns: []string{"ACTIVE", "NAME", "SITE", "AUTH", "KEYS"},
}

// overviewResource is the :overview pseudo-resource — cross-resource triage:
// open incidents + alerting monitors, merged across every active org (worst
// first). Served by a dedicated loader that reuses the per-resource caches,
// so it costs nothing beyond the underlying incidents/monitors fetches.
var overviewResource = data.Resource{
	Key:     "overview",
	Title:   "Overview",
	Columns: []string{"TYPE", "STATUS", "TITLE", "CREATED"},
	TTL:     60 * time.Second, AutoRefresh: true,
}

// App is the k9s-style shell: header (info + hints), one resource table,
// a command/filter prompt and a status line.
type App struct {
	*tview.Application
	provider *data.Cached
	// providers holds one cache per active context (current + explicitly
	// activated), keyed by context name. Spanning views fan out over it;
	// providerFor routes row-scoped calls to the row's origin org.
	providers    map[string]*data.Cached
	opts         Options
	theme        Theme
	ctxInfos     []ContextInfo
	current      string // current context name (always active)
	refreshEvery time.Duration

	content    *tview.Pages // "table" | "detail" | "help" | "ctxform" (+ "confirm" overlay)
	infoTV     *tview.TextView
	hintTV     *tview.TextView
	table      *tview.Table
	prompt     *tview.InputField
	status     *tview.TextView
	footer     *tview.Pages
	detail     *tview.TextView
	dash       *tview.TextView
	trace      *tview.TextView
	patterns   *tview.TextView
	splash     *tview.TextView // startup logo, auto-dismissed
	rootView   tview.Primitive // the normal layout (header + content + footer)
	splashView tview.Primitive // full-screen splash overlay (shown briefly at launch)
	ctxForm    *tview.Form
	formErr    *tview.TextView
	// editingCtx is the context name being edited in the :ctx form ("" = adding
	// a new one). ctxFormBuilding suppresses the Auth dropdown's rebuild
	// callback while the form is being (re)constructed.
	editingCtx      string
	ctxFormBuilding bool
	// introMarked: MarkIntroSeen already ran this session (first-run intro).
	introMarked bool
	// splashReturn is the page the startup splash covered, restored on dismiss.
	splashReturn string
	confirm      *tview.Modal
	savedQL      *tview.List  // the 'Q' saved-query picker
	savedQV      string       // view the open picker is scoped to
	savedQIt     []SavedQuery // items backing the picker, by list index
	pendSaveQ    string       // query pending a name (save prompt in flight)

	settingsTbl *tview.Table // the :settings editor
	settingRows []settingRow // editable settings, indexed by table data row
	editingSet  int          // settingRows index being edited (prompt in flight)

	todoIncidentID string // incident whose to-dos the panel shows / to-do add in flight
	todoCtx        string // that incident's origin context (multi-org routing)

	colPick      *tview.List // the 'C' column picker
	colPickView  string      // view whose columns the picker is editing
	colPickItems []colItem   // picker rows (column + shown), in display order

	// userpick: the searchable commander/assignee picker ('I', to-do assign)
	userPickFlex    *tview.Flex
	userSearch      *tview.InputField
	userPick        *tview.List
	userPickItems   []data.User           // list rows, in display order
	userPickOnPick  func(data.User)       // what to do with the chosen user
	userPickReturn  string                // page to return to on pick/cancel
	userPickSeq     int                   // drops stale async search results
	userSearchTimer *time.Timer           // search debounce
	userPickCtx     string                // org the picker searches (multi-org routing)
	pickSelf        map[string]*data.User // acting user per org, pinned atop an empty search
	pendTodoContent string                // to-do content awaiting an assignee pick

	// todos: the incident to-do panel ('T')
	todoList  *tview.List
	todoItems []data.Todo // list rows, in display order

	confirmReturn string // page to restore after a confirm modal closes

	// fuzzy: the 'F' fuzzy row finder (type to match, enter jumps to the row)
	fuzzyFlex  *tview.Flex
	fuzzyInput *tview.InputField
	fuzzyList  *tview.List
	fuzzyRows  []int // a.rows indices backing the list, in display order

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
		providers:    map[string]*data.Cached{},
		opts:         o,
		ctxInfos:     o.Contexts,
		current:      o.Current,
		refreshEvery: o.Refresh,
		queries:      map[string]string{},
		history:      map[string][]string{},
	}
	a.providers[o.Current] = a.provider
	// Bring up providers for contexts persisted as active. A failure only
	// deactivates that context (with a log); it never blocks startup.
	for i, c := range o.Contexts {
		if !c.Active || c.Name == o.Current {
			continue
		}
		ap, err := o.Factory(c.Name)
		if err != nil {
			slog.Warn("active context unavailable, deactivating", "context", c.Name, "err", err)
			a.ctxInfos[i].Active = false
			continue
		}
		a.providers[c.Name] = data.NewCached(ap)
	}
	a.build()
	if startErr != nil {
		a.showContexts()
		a.flash("✗ context "+o.Current+": "+startErr.Error()+" — press <a> to add a context", true)
		if o.FirstRun {
			a.showIntro() // one-time getting-started page; esc drops into :ctx
		}
	} else {
		a.switchResource(initialResource(o.CurrentView)) // restore the last view
		if o.FirstRun {
			// Shown BEFORE the splash: the splash restores the page it covered,
			// so it dismisses into the getting-started page.
			a.showIntro()
		}
		a.showSplash() // brief logo over the loading view
	}
	go a.ticker()
	return a, nil
}

// initialResource resolves the persisted view name to a resource, falling back
// to the first registered resource when empty or unknown.
func initialResource(view string) data.Resource {
	if r, ok := data.ResourceByAlias(view); ok {
		return r
	}
	return data.Resources()[0]
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
	if a.fuzzyFlex != nil {
		a.fuzzyFlex.SetBorderColor(a.theme.Border)
		a.fuzzyFlex.SetTitleColor(a.theme.Title)
		a.fuzzyInput.SetLabelColor(a.theme.Label)
		a.fuzzyInput.SetFieldBackgroundColor(a.theme.FieldBg)
		a.fuzzyInput.SetFieldTextColor(a.theme.FieldFg)
		a.fuzzyList.SetSelectedStyle(sel)
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

	a.splash = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	// Transparent so the splash inherits the terminal background instead of
	// painting a differently-shaded rectangle behind the logo.
	a.splash.SetBackgroundColor(tcell.ColorDefault)
	// Full-screen splash: flexible spacers above/below a fixed block centre the
	// logo vertically. Shown as its own root (not a content page) so the tall
	// art isn't boxed under the header/footer. The wrapper is transparent too so
	// the whole splash area matches the terminal (no painted band).
	splashFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	splashFlex.AddItem(nil, 0, 1, false).
		AddItem(a.splash, splashHeight, 0, false).
		AddItem(nil, 0, 1, false)
	splashFlex.SetBackgroundColor(tcell.ColorDefault)
	a.splashView = splashFlex

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

	// fuzzy: search field over ranked row matches. See fuzzy.go.
	a.fuzzyInput = tview.NewInputField().SetLabel(" fuzzy> ")
	a.fuzzyInput.SetChangedFunc(func(string) { a.renderFuzzy() })
	a.fuzzyList = tview.NewList().ShowSecondaryText(false)
	a.fuzzyList.SetMainTextColor(tcell.ColorWhite)
	a.fuzzyFlex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.fuzzyInput, 1, 0, true).
		AddItem(a.fuzzyList, 0, 1, false)
	a.fuzzyFlex.SetBorder(true).SetTitle(" Fuzzy find ")

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
		AddPage("fuzzy", a.fuzzyFlex, true, false).
		AddPage("help", a.buildHelp(), true, false).
		AddPage("intro", a.buildIntro(), true, false).
		AddPage("ctxform", ctxFormFlex, true, false).
		AddPage("confirm", a.confirm, true, false)
	a.page = "table"

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(a.content, 0, 1, true).
		AddItem(a.footer, 1, 0, false)
	a.rootView = main

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
		case "rum":
			lines = append(lines, "[gray]</>RUM query (e.g. @type:error)  window: <1>15m..<5>7d  <s>sort")
		case "synthetics":
			lines = append(lines, "[gray]<enter>latest results + pass rate  <s>sort <S>reverse")
		case overviewResource.Key:
			lines = append(lines, "[gray]<enter>detail  open incidents + alerting monitors across every active org")
		case ctxResource.Key:
			lines = append(lines, "[gray]<enter>switch org  <space>toggle active (all active orgs merge in views)  <O>browser sign-in  <a>add  <e>edit  <d>delete")
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
                 events rum synthetics downtimes dashboards (aliases: mon inc s l tr svc ev dt d syn)
                 — :overview (cross-org triage) / :ctx / :settings
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
   [aqua]F[white]             (any table) fuzzy row finder — type a subsequence, [aqua]enter[white] jumps to the row

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
   [aqua]enter[white]         switch to an org (cache, budget and history reset). Orgs marked
                 active with space stay in; anything else drops out
   [aqua]space[white]         toggle an org active — every "active" row merges into the views
                 (CTX column names each row's org); actions on a row always hit
                 that row's org. The org you're driving shows in the header
   [aqua]O[white]             browser sign-in (OAuth) for the selected context — tokens go to the OS
                 keychain and refresh automatically. On an OAuth row it signs in or
                 refreshes; on a key/token row it offers to convert it (asks first)
   [aqua]a[white]             add a context — pick its auth (browser sign-in, API/APP keys, or
                 access token); the form's fields follow the choice
   [aqua]e[white]             edit the selected context in a form (auth type, site, subdomain,
                 credentials) — leave a secret field empty to keep the stored one
   [aqua]d[white] (or ctrl-d) delete the selected context (asks first)

 [orange]OTHER
   [aqua]C[white]             (any table) column picker — [aqua]space[white] show/hide, [aqua]J/K[white] reorder; live + saved
   [aqua]:settings[white]     theme and per-view cache TTLs — applies live + saved to config
   [aqua]?[white]             this help (from any view)
   [aqua]:manual[white]       the getting-started page (shown once on first run)
   [aqua]q[white]             back in detail/help; quit from a table view
   [aqua]ctrl-c[white]        quit — press twice to confirm (also :q :quit :exit)

 [gray]Views auto-refresh only where cheap (monitors, incidents), else cached per TTL.
 [gray]The Budget header shows Datadog X-RateLimit headroom; a 429 auto-pauses refresh.
`))
	return tv
}

// buildIntro is the getting-started page: shown once on first run and
// reopenable any time with :manual (or :instructions). Deliberately shorter
// and more task-oriented than the full help (?).
func (a *App) buildIntro() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Getting started ").SetTitleColor(a.theme.Title)
	fmt.Fprint(tv, a.theme.recolor(`
 [orange]ike — keep an eye on your Datadog, k9s-style.

 [orange]1 · Connect an org[white]
   [aqua]:ctx[white] then [aqua]a[white] adds a context. Pick the auth type — [aqua]Browser sign-in
   (OAuth)[white] is the easy path: no keys to paste, tokens refresh themselves.
   [aqua]O[white] on a row signs in again whenever needed.

 [orange]2 · Look around[white]
   [aqua]:monitors :incidents :slos :logs :traces :services :events :rum
   :synthetics :downtimes :dashboards[white] switch views; [aqua]:overview[white] is
   cross-org triage. [aqua]enter[white] drills into a row, [aqua]esc[white] goes back,
   [aqua]o[white] opens the row in the Datadog web UI.

 [orange]3 · Filter and find[white]
   [aqua]/[white] filters the table — in logs, traces and events it is a full
   Datadog query (server-side). [aqua]F[white] fuzzy-finds in the current view.

 [orange]4 · Watch several orgs at once[white]
   In [aqua]:ctx[white], [aqua]space[white] marks orgs active — active rows highlight and every
   view merges them (the CTX column names each row's org). [aqua]enter[white]
   switches org: marked orgs stay in, everything else drops out.

 [orange]5 · Act — every write asks first[white]
   [aqua]m[white] mute monitor · [aqua]r[white]/[aqua]v[white] incident state/severity · [aqua]I[white] commander ·
   [aqua]T[white] to-dos · [aqua]x[white] cancel downtime

 [gray]? opens the full key reference · :manual reopens this page any time.
 [gray]No credentials yet? Quit and run ike --demo to explore with fake data.
 [gray]<esc> starts you off.`))
	return tv
}

// showIntro opens the getting-started page and, on a first run, persists that
// it was shown so the next session starts normally.
func (a *App) showIntro() {
	if a.page == "intro" {
		return
	}
	a.pushNav()
	a.showPage("intro")
	if a.opts.FirstRun && !a.introMarked {
		a.introMarked = true
		if a.opts.MarkIntroSeen != nil {
			if err := a.opts.MarkIntroSeen(); err != nil {
				slog.Warn("persist intro-seen failed", "err", err)
			}
		}
	}
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
	case "splash":
		a.dismissSplash() // any key skips the splash (the key is swallowed)
		return nil
	case "help", "intro":
		if ev.Key() == tcell.KeyEscape || ev.Rune() == 'q' {
			a.back()
			return nil
		}
		return ev
	case "ctxform":
		if ev.Key() == tcell.KeyEscape {
			if a.ctxDropdownOpen() {
				return ev // an open dropdown closes itself first; keep the form
			}
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
	case "fuzzy":
		switch {
		case ev.Key() == tcell.KeyEscape:
			a.closeFuzzy(false)
			return nil
		case ev.Key() == tcell.KeyEnter:
			a.closeFuzzy(true)
			return nil
		case ev.Key() == tcell.KeyUp:
			a.fuzzyMove(-1)
			return nil
		case ev.Key() == tcell.KeyDown:
			a.fuzzyMove(1)
			return nil
		}
		return ev // everything else types into the query
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
	case 'O':
		if a.res.Key == ctxResource.Key {
			a.beginRowLogin()
			return nil
		}
	case 'e':
		if a.res.Key == ctxResource.Key {
			a.openEditForm()
			return nil
		}
	case 'd':
		if a.res.Key == ctxResource.Key {
			a.confirmDeleteContext() // plain-key alias for ctrl-d (confirm-gated)
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
		if a.res.Key == "logs" || a.res.Key == "traces" || a.res.Key == "events" || a.res.Key == "rum" {
			a.setLogRange(ev.Rune())
			return nil
		}
		if a.res.Key == "incidents" {
			a.incidentQuickFilter(ev.Rune())
			return nil
		}
	case '5':
		if a.res.Key == "logs" || a.res.Key == "traces" || a.res.Key == "events" || a.res.Key == "rum" {
			a.setLogRange(ev.Rune())
			return nil
		}
	case 'c':
		a.copySelected()
		return nil
	case 'F':
		if a.res.Key != ctxResource.Key {
			a.openFuzzy()
			return nil
		}
	case ' ':
		if a.res.Key == ctxResource.Key {
			if row, ok := a.selectedRow(); ok {
				a.toggleContextActive(row.ID)
			}
			return nil
		}
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
				a.openTodoPanel(row)
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
				err := a.providerFor(r).SetMonitorMute(context.Background(), r.ID, !r.Muted)
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
		// Pick the assignee (self pinned on top) before creating the to-do,
		// searching the incident's own org.
		a.userPickCtx = a.todoCtx
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
	case "manual", "instructions", "intro", "tutorial":
		a.showIntro()
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
	if cmd == "overview" || cmd == "ov" {
		a.switchResource(overviewResource)
		return
	}
	if res, ok := data.ResourceByAlias(cmd); ok {
		a.switchResource(res)
		a.persistSession() // remember this view for the next session
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
	names = append(names, "ctx", "overview", "settings", "help", "manual", "quit")
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
	prov := a.providerFor(a.detailRow) // the drilled-from row's org
	go func() {
		start := time.Now()
		v, err := prov.Trace(context.Background(), traceID)
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
		// One state, one word: every org participating in views reads
		// "active" — the org you switched to (enter) and any space-marked
		// ones alike. Blank = not participating. Which org you're driving
		// is already in the header (Mode: live [name]).
		status := ""
		if c.Name == a.current || c.Active {
			status = "active"
		}
		rows = append(rows, data.Row{
			ID:    c.Name,
			Cells: []string{status, c.Name, c.Site, authLabel(c.Auth), c.Keys},
			Raw:   map[string]any{"name": c.Name, "site": c.Site, "auth": authLabel(c.Auth), "keys": c.Keys, "active": c.Active},
		})
	}
	return rows
}

// authLabel is the :ctx AUTH column value for a stored auth shape.
func authLabel(auth string) string {
	switch auth {
	case "oauth":
		return "oauth"
	case "token":
		return "token"
	default:
		return "keys"
	}
}

// ctxActive reports a context's explicit-activation flag (space in :ctx).
func (a *App) ctxActive(name string) bool {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			return c.Active
		}
	}
	return false
}

// activeEntries returns the active providers in stable order: the current
// context first, then explicitly-activated contexts in config order.
func (a *App) activeEntries() []ctxProvider {
	out := []ctxProvider{{a.current, a.provider}}
	for _, c := range a.ctxInfos {
		if c.Name == a.current || !c.Active {
			continue
		}
		if p, ok := a.providers[c.Name]; ok {
			out = append(out, ctxProvider{c.Name, p})
		}
	}
	return out
}

type ctxProvider struct {
	name string
	p    *data.Cached
}

// spanningResources are the views that merge rows across active orgs. Every
// provider-backed view spans (stage 2); each org spends only its own
// rate-limit budget, so a fan-out costs one call per org, not N per org.
var spanningResources = map[string]bool{
	"monitors": true, "incidents": true, "slos": true, "downtimes": true,
	"logs": true, "traces": true, "events": true, "services": true, "rum": true,
	"synthetics": true,
	"dashboards": true, "overview": true,
}

// spanning reports whether the current view should fan out over active orgs.
func (a *App) spanning() bool {
	return spanningResources[a.res.Key] && len(a.activeEntries()) > 1
}

// providerFor routes a row-scoped call (detail, drill-down, write) to the
// row's origin org; rows without a Ctx belong to the current context.
func (a *App) providerFor(r data.Row) *data.Cached {
	if r.Ctx != "" {
		if p, ok := a.providers[r.Ctx]; ok {
			return p
		}
	}
	return a.provider
}

// toggleContextActive flips a context's explicit activation (space in :ctx).
// Activating brings up its provider; deactivating tears it down (unless it is
// the current context, which stays active implicitly — the flag then only
// controls whether it survives a current-context switch).
func (a *App) toggleContextActive(name string) {
	for i, c := range a.ctxInfos {
		if c.Name != name {
			continue
		}
		if !c.Active { // activate
			if name != a.current {
				p, err := a.opts.Factory(name)
				if err != nil {
					a.flash("✗ context "+name+": "+err.Error(), true)
					return
				}
				a.providers[name] = data.NewCached(p)
			}
			a.ctxInfos[i].Active = true
			if name == a.current {
				a.flash("context "+name+" marked — it will stay active when you switch to another org", false)
			} else {
				a.flash("context "+name+" activated — spanning views merge it", false)
			}
		} else { // deactivate
			a.ctxInfos[i].Active = false
			if name != a.current {
				delete(a.providers, name) // hard teardown, same boundary as a switch
				a.flash("context "+name+" deactivated", false)
			} else {
				// The driven org can't leave the views — its row stays "active".
				// Without this message a space here looks like a no-op bug.
				a.flash("context "+name+" stays active while you're driving it — it drops out when you switch away", false)
			}
		}
		if a.opts.PersistActive != nil {
			if err := a.opts.PersistActive(name, a.ctxInfos[i].Active); err != nil {
				slog.Warn("persist active failed", "context", name, "err", err)
			}
		}
		a.load(false) // refresh the :ctx table markers
		return
	}
}

// switchContext moves the current context. The target keeps its cache if it
// was already active; the old current is torn down unless explicitly active
// (space) — so single-active usage behaves exactly like the old hard switch,
// while activated orgs survive. Navigation history and queries always reset.
func (a *App) switchContext(name string) {
	if name == a.current {
		a.flash("already on context "+name, false)
		return
	}
	p, ok := a.providers[name]
	if !ok {
		np, err := a.opts.Factory(name)
		if err != nil {
			slog.Error("context switch failed", "to", name, "err", err)
			// An OAuth context with no tokens yet isn't an error the user should
			// see raw — it just hasn't been signed into. Point them at 'O'.
			if a.ctxAuth(name) == "oauth" {
				a.flash("context "+name+" is not signed in yet — press O to sign in", false)
			} else {
				a.flash("✗ context "+name+": "+err.Error(), true)
			}
			return
		}
		p = data.NewCached(np)
	}
	slog.Info("context switch", "from", a.current, "to", name)
	old := a.current
	if !a.ctxActive(old) {
		delete(a.providers, old)
	}
	a.providers[name] = p
	a.provider = p
	a.current = name
	a.stack = nil
	a.queries = map[string]string{}
	a.detailRow = data.Row{}
	a.res = data.Resource{} // so switchResource doesn't push the ctx view
	a.flash("context → "+name, false)
	a.switchResource(data.Resources()[0])
	a.persistSession() // remember the new org (+ its reset-to-first view)
}

// persistSession writes the active org + view so a new session reopens here.
// Called at deliberate navigation points (org switch, :view switch), never for
// transient drill-downs. The ctx switcher and the empty resource are skipped.
func (a *App) persistSession() {
	if a.opts.PersistSession == nil {
		return
	}
	if a.res.Key == "" || a.res.Key == ctxResource.Key {
		return
	}
	if err := a.opts.PersistSession(a.current, a.res.Key); err != nil {
		slog.Warn("persist session failed", "context", a.current, "view", a.res.Key, "err", err)
	}
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
	case "fuzzy":
		a.SetFocus(a.fuzzyInput)
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

// ctxAuthOptions are the auth shapes offered in the context form, in dropdown
// order. Index 0 (OAuth) is the default and the recommended path. The indices
// are the authMode contract with UpdateContext ("oauth"/"keys"/"token").
var ctxAuthOptions = []string{"Browser sign-in (OAuth)", "API + APP keys", "Access token"}

const authModeOAuth, authModeKeys, authModeToken = 0, 1, 2

// authModeFor maps a stored Auth string to a dropdown index.
func authModeFor(auth string) int {
	switch auth {
	case "oauth":
		return authModeOAuth
	case "token":
		return authModeToken
	default:
		return authModeKeys
	}
}

// authModeName maps a dropdown index to the authMode string UpdateContext takes.
func authModeName(mode int) string {
	switch mode {
	case authModeOAuth:
		return "oauth"
	case authModeToken:
		return "token"
	default:
		return "keys"
	}
}

// openCtxForm shows the add-context form (:ctx → a): a new context, defaulting
// to OAuth. Secret fields go to the OS keychain, never the config file.
func (a *App) openCtxForm() {
	if a.opts.AddContext == nil {
		a.flash("adding contexts is not available in this mode", true)
		return
	}
	a.showCtxForm("", authModeOAuth, ContextInfo{})
}

// openEditForm shows the edit form for the selected context (:ctx → e),
// pre-filled and defaulting to its current auth type.
func (a *App) openEditForm() {
	if a.opts.UpdateContext == nil {
		a.flash("editing contexts is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	var info ContextInfo
	for _, c := range a.ctxInfos {
		if c.Name == r.ID {
			info = c
		}
	}
	if info.Name == "" {
		return
	}
	a.showCtxForm(info.Name, authModeFor(info.Auth), info)
}

// showCtxForm builds the shared add/edit context form. editing is "" when
// adding; otherwise the name being edited (its Name field is locked). mode is
// the initial Auth selection; v pre-fills the common fields.
func (a *App) showCtxForm(editing string, mode int, v ContextInfo) {
	a.pushNav()
	a.formErr.SetText("")
	a.editingCtx = editing
	a.ctxForm.Clear(true)
	a.ctxFormBuilding = true
	a.ctxForm.AddDropDown("Auth", ctxAuthOptions, mode, func(_ string, idx int) {
		if a.ctxFormBuilding {
			return
		}
		// Preserve the common fields the user already filled, then rebuild the
		// credential fields for the newly-chosen mode.
		cur := ContextInfo{
			Name:      a.ctxFieldText("Name"),
			Site:      a.ctxSelectedSite(),
			Subdomain: a.ctxFieldText("Subdomain (optional)"),
		}
		a.rebuildCtxBody(idx, cur)
		a.SetFocus(a.ctxForm)
	})
	if dd, ok := a.ctxForm.GetFormItemByLabel("Auth").(*tview.DropDown); ok {
		dropdownNoArrowOpen(dd)
	}
	a.rebuildCtxBody(mode, v)
	a.ctxFormBuilding = false
	if editing == "" {
		a.ctxForm.SetTitle(" Add context ")
	} else {
		a.ctxForm.SetTitle(" Edit context: " + editing + " ")
	}
	a.showPage("ctxform")
}

// rebuildCtxBody rebuilds the form's fields below the Auth dropdown for the
// given mode. The dropdown at index 0 is never touched, so this is safe to
// call from the dropdown's own selection callback.
func (a *App) rebuildCtxBody(mode int, v ContextInfo) {
	for a.ctxForm.GetFormItemCount() > 1 {
		a.ctxForm.RemoveFormItem(a.ctxForm.GetFormItemCount() - 1)
	}
	a.ctxForm.ClearButtons()

	labels := make([]string, len(config.Sites))
	for i, s := range config.Sites {
		labels[i] = fmt.Sprintf("%-17s (%s)", s, siteRegions[s])
	}
	siteIdx := 0
	for i, s := range config.Sites {
		if s == v.Site {
			siteIdx = i
		}
	}

	// When editing, the name is the keychain/config key (rename is out of
	// scope) so it isn't an editable field — it's in the form title instead.
	if a.editingCtx == "" {
		a.ctxForm.AddInputField("Name", v.Name, 30, nil, nil)
	}
	a.ctxForm.AddDropDown("Site", labels, siteIdx, nil)
	if dd, ok := a.ctxForm.GetFormItemByLabel("Site").(*tview.DropDown); ok {
		dropdownNoArrowOpen(dd)
	}
	switch mode {
	case authModeKeys:
		a.ctxForm.
			AddPasswordField("API key", "", 50, '*', nil).
			AddPasswordField("APP key", "", 50, '*', nil)
	case authModeToken:
		a.ctxForm.AddPasswordField("Access token", "", 50, '*', nil)
	}
	a.ctxForm.AddInputField("Subdomain (optional)", v.Subdomain, 30, nil, nil)

	save := "Save"
	if mode == authModeOAuth {
		if a.editingCtx == "" {
			save = "Sign in with browser"
		} else {
			save = "Save & sign in"
		}
	}
	a.ctxForm.AddButton(save, a.submitCtxForm).AddButton("Cancel", a.back)
}

// ctxDropdownOpen reports whether either dropdown in the context form has its
// list open — used so <esc> closes the list before it closes the form.
func (a *App) ctxDropdownOpen() bool {
	for _, label := range []string{"Auth", "Site"} {
		if dd, ok := a.ctxForm.GetFormItemByLabel(label).(*tview.DropDown); ok && dd.IsOpen() {
			return true
		}
	}
	return false
}

// dropdownNoArrowOpen keeps a closed dropdown from opening on Up/Down — those
// move between form fields like every other item instead (the list still opens
// on enter or space). Once the list is open, arrows pass through to navigate
// options as usual.
func dropdownNoArrowOpen(dd *tview.DropDown) {
	dd.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if dd.IsOpen() {
			return ev
		}
		switch ev.Key() {
		case tcell.KeyDown:
			return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
		case tcell.KeyUp:
			return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
		}
		return ev
	})
}

// ctxFieldText reads an input/password field by label ("" if the field is
// absent in the current mode).
func (a *App) ctxFieldText(label string) string {
	if it := a.ctxForm.GetFormItemByLabel(label); it != nil {
		if inp, ok := it.(*tview.InputField); ok {
			return inp.GetText()
		}
	}
	return ""
}

// ctxSelectedSite returns the site chosen in the Site dropdown.
func (a *App) ctxSelectedSite() string {
	if it := a.ctxForm.GetFormItemByLabel("Site"); it != nil {
		if dd, ok := it.(*tview.DropDown); ok {
			if idx, _ := dd.GetCurrentOption(); idx >= 0 && idx < len(config.Sites) {
				return config.Sites[idx]
			}
		}
	}
	return ""
}

// beginRowLogin runs the browser sign-in for the selected :ctx row ('O'). An
// OAuth context signs in (or refreshes) directly; a key/token context is a
// conversion, so it asks first before switching that context to OAuth.
func (a *App) beginRowLogin() {
	if a.opts.OAuthLogin == nil {
		a.flash("browser sign-in is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	name := r.ID
	if a.ctxAuth(name) == "oauth" {
		a.startLogin(name)
		return
	}
	using := "an API + APP key pair"
	if a.ctxAuth(name) == "token" {
		using = "an access token"
	}
	a.showConfirm(
		fmt.Sprintf("Context %q signs in with %s.\nBrowser sign-in will replace that with OAuth for this context.\nContinue?", name, using),
		[]string{"Cancel", "Sign in with browser"},
		func(label string) {
			if label == "Sign in with browser" {
				a.startLogin(name)
			}
		})
}

// startLogin kicks off the blocking browser flow for one context off the UI
// thread and folds the refreshed info back into :ctx. The flash names the host
// being opened so it's clear the org's subdomain (not app.<site>) is used.
func (a *App) startLogin(name string) {
	a.flash("browser opened for "+name+" → "+a.ctxAuthHost(name)+" — complete the sign-in there …", false)
	go func() {
		info, err := a.opts.OAuthLogin(name)
		a.QueueUpdateDraw(func() {
			if err != nil {
				a.flash("✗ sign-in: "+err.Error(), true)
				return
			}
			replaced := false
			for i, c := range a.ctxInfos {
				if c.Name == info.Name {
					a.ctxInfos[i] = info
					replaced = true
				}
			}
			if !replaced {
				a.ctxInfos = append(a.ctxInfos, info)
			}
			if a.res.Key == ctxResource.Key {
				a.load(false)
			}
			a.flash("signed in — context "+info.Name+" ready (enter to switch)", false)
		})
	}()
}

// ctxAuth returns a context's auth shape ("oauth" / "token" / "" for keys).
func (a *App) ctxAuth(name string) string {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			return c.Auth
		}
	}
	return ""
}

// ctxAuthHost is the browser host the OAuth sign-in opens for a context: its
// custom subdomain when set, else app.<site>. Mirrors auth.EndpointsFor so the
// flash matches the URL actually opened.
func (a *App) ctxAuthHost(name string) string {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			if c.Subdomain != "" {
				return c.Subdomain + "." + c.Site
			}
			return "app." + c.Site
		}
	}
	return name
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

[aqua]Auth[white] — how this context signs in. The credential fields below change to match your choice:

[yellow]Browser sign-in (OAuth)[white] (recommended) — no keys to paste. The button signs you in through your browser; tokens go to the OS keychain and refresh automatically.

[yellow]API + APP keys[white] — fill BOTH key fields.
[green]API key[white]: Organization Settings → API Keys (org-wide; ask an admin if you cannot create one).
[green]APP key[white]: Personal Settings → Application Keys → New Key. Scope it read-only: monitors_read, incidents_read, slos_read, logs_read_data, dashboards_read.

[yellow]Access token[white] — a bearer token (OAuth2 access token or PAT, e.g. from Datadog's pup CLI or your SSO tooling). Usually short-lived (~1h).

[aqua]Name[white] — anything you like ("Datadog Dev", "prod", …). Locked when editing.

[aqua]Site[white] — pick from the list (enter/space or click opens it). It matches the region in your Datadog URL: app.[green]datadoghq.eu[white] → datadoghq.eu.

[aqua]Subdomain[white] — only if your org's web UI lives on a custom subdomain: for https://[green]acme-stage[white].datadoghq.eu enter [green]acme-stage[white]. The browser sign-in opens on this host, and 'open in Datadog' links use it; API calls are unaffected. Leave empty if your URL starts with app.

[gray]When editing, leave a credential field empty to keep the stored secret. Secrets go to the OS keychain (service "ike"), never into the config file. <esc> cancels.`

// submitCtxForm validates and applies the :ctx form for both add and edit.
// Fields are read by label because which credential fields exist depends on
// the chosen auth mode.
func (a *App) submitCtxForm() {
	mode := authModeOAuth
	if it := a.ctxForm.GetFormItemByLabel("Auth"); it != nil {
		if dd, ok := it.(*tview.DropDown); ok {
			mode, _ = dd.GetCurrentOption()
		}
	}
	editing := a.editingCtx
	name := strings.TrimSpace(a.ctxFieldText("Name"))
	if editing != "" {
		name = editing // locked in edit mode
	}
	site := a.ctxSelectedSite()
	if site == "" {
		site = config.Sites[0]
	}
	apiKey := a.ctxFieldText("API key")
	appKey := a.ctxFieldText("APP key")
	token := a.ctxFieldText("Access token")
	subdomain := strings.TrimSpace(a.ctxFieldText("Subdomain (optional)"))

	if name == "" {
		a.formError("Name is required")
		return
	}
	if !config.ValidSubdomain(subdomain) {
		a.formError("subdomain must be a single DNS label, e.g. acme-stage (from https://acme-stage." + site + ")")
		return
	}
	if editing == "" {
		for _, c := range a.ctxInfos {
			if c.Name == name {
				a.formError("context " + name + " already exists")
				return
			}
		}
	}

	// Whether the credential fields must be filled: always for a new key/token
	// context; on edit, only when switching INTO a mode the context doesn't
	// already store in the keychain (empty means "keep the stored secret").
	credsRequired := true
	if editing != "" {
		for _, c := range a.ctxInfos {
			if c.Name == editing && authModeFor(c.Auth) == mode && strings.HasPrefix(c.Keys, "keychain") {
				credsRequired = false
			}
		}
	}
	if mode == authModeKeys && credsRequired && (apiKey == "" || appKey == "") {
		a.formError("API keys selected: fill BOTH the API key and the APP key")
		return
	}
	if mode == authModeToken && credsRequired && token == "" {
		a.formError("access token selected: fill the Access token field")
		return
	}
	if mode != authModeKeys {
		apiKey, appKey = "", ""
	}
	if mode != authModeToken {
		token = ""
	}

	if editing != "" {
		a.submitEditCtx(mode, site, apiKey, appKey, token, subdomain)
		return
	}
	a.submitAddCtx(mode, name, site, apiKey, appKey, token, subdomain)
}

// submitAddCtx handles the add path: OAuth creates a pending context and signs
// in from the form; keys/token persist to the keychain.
func (a *App) submitAddCtx(mode int, name, site, apiKey, appKey, token, subdomain string) {
	if mode == authModeOAuth {
		if a.opts.AddOAuthContext == nil {
			a.formError("browser sign-in is not available in this mode")
			return
		}
		info, err := a.opts.AddOAuthContext(name, site, subdomain)
		if err != nil {
			a.formError(err.Error())
			return
		}
		slog.Info("oauth context added", "name", name, "site", site)
		a.ctxInfos = append(a.ctxInfos, info)
		a.back()
		a.startLogin(name) // the form's button IS the sign-in
		return
	}
	info, err := a.opts.AddContext(name, site, apiKey, appKey, token, subdomain)
	if err != nil {
		a.formError(err.Error())
		return
	}
	slog.Info("context added", "name", name, "site", site, "auth", authModeName(mode))
	a.ctxInfos = append(a.ctxInfos, info)
	a.back()
	a.flash("context "+name+" added — enter on it to switch", false)
}

// submitEditCtx handles the edit path: UpdateContext persists the changes; an
// OAuth edit also (re-)runs the browser sign-in.
func (a *App) submitEditCtx(mode int, site, apiKey, appKey, token, subdomain string) {
	name := a.editingCtx
	info, err := a.opts.UpdateContext(name, authModeName(mode), site, apiKey, appKey, token, subdomain)
	if err != nil {
		a.formError(err.Error())
		return
	}
	slog.Info("context updated", "name", name, "site", site, "auth", authModeName(mode))
	for i, c := range a.ctxInfos {
		if c.Name == name {
			a.ctxInfos[i] = info
		}
	}
	a.back()
	if mode == authModeOAuth {
		a.startLogin(name)
		return
	}
	a.flash("context "+name+" updated", false)
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
		err := a.providerFor(r).SetIncidentField(context.Background(), r.ID, field, value)
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
	a.userPickCtx = r.Ctx
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
		err := a.providerFor(r).SetIncidentCommander(context.Background(), r.ID, u.ID)
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
		err := a.todoProv().AddIncidentTodo(context.Background(), incidentID, content, handle)
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
		err := a.providerFor(r).CancelDowntime(context.Background(), r.ID)
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
	if a.res.Key == overviewResource.Key {
		a.loadOverview(force)
		return
	}
	if a.loading {
		return
	}
	a.loading = true
	res, q := a.res, a.queries[a.res.Key]
	tr := a.timeRange()
	entries := a.activeEntries()
	span := spanningResources[res.Key] && len(entries) > 1
	if !span {
		entries = entries[:1] // current context only
	}
	go func() {
		start := time.Now()
		type orgResult struct {
			name   string
			rows   []data.Row
			at     time.Time
			cached bool
			err    error
		}
		results := make([]orgResult, len(entries))
		var wg sync.WaitGroup
		for i, e := range entries {
			wg.Add(1)
			go func(i int, e ctxProvider) {
				defer wg.Done()
				rows, at, cached, err := e.p.Fetch(context.Background(), res, q, tr, force)
				for j := range rows {
					rows[j].Ctx = e.name
				}
				results[i] = orgResult{e.name, rows, at, cached, err}
			}(i, e)
		}
		wg.Wait()
		var rows []data.Row
		var at time.Time
		cached := true
		var firstErr error
		for _, r := range results {
			rows = append(rows, r.rows...)
			if r.at.After(at) {
				at = r.at
			}
			if !r.cached {
				cached = false
			}
			if r.err != nil && firstErr == nil {
				if len(entries) > 1 {
					firstErr = fmt.Errorf("%s: %w", r.name, r.err)
				} else {
					firstErr = r.err
				}
			}
		}
		if span {
			mergeOrder(res.Key, rows)
		}
		err := firstErr
		slog.Debug("fetch", "resource", res.Key, "query", q, "range", tr, "force", force, "orgs", len(entries),
			"rows", len(rows), "cached", cached, "took", time.Since(start).Round(time.Millisecond), "err", err)
		a.QueueUpdateDraw(func() {
			a.loading = false
			if a.res.Key != res.Key {
				return // user switched view while fetching
			}
			if err != nil {
				a.flash("✗ "+err.Error(), true)
				// A 429 means an org's shared budget is exhausted — stop the
				// auto-refresh timer from making it worse.
				if data.ErrorIsRateLimit(err) && !a.paused {
					a.paused = true
					a.setHints()
					slog.Warn("auto-refresh paused: rate limited")
				}
				if rows == nil {
					return // no org returned anything (stale rows included)
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

// mergeOrder restores a view's natural order after a cross-org merge: monitors
// re-sort Alert-first; time-lined views newest-first; name-keyed views
// alphabetical; the rest keep the per-org grouping (stable fan-out order).
func mergeOrder(key string, rows []data.Row) {
	byCell := func(i int, desc bool) {
		sort.SliceStable(rows, func(a, b int) bool {
			ca, cb := "", ""
			if len(rows[a].Cells) > i {
				ca = rows[a].Cells[i]
			}
			if len(rows[b].Cells) > i {
				cb = rows[b].Cells[i]
			}
			if desc {
				return ca > cb
			}
			return ca < cb
		})
	}
	switch key {
	case "monitors":
		data.SortMonitors(rows)
	case "incidents":
		byCell(5, true) // CREATED, newest first (timestamps sort lexically)
	case "logs", "traces", "events":
		byCell(0, true) // TIME, newest first
	case "services", "dashboards":
		byCell(0, false) // SERVICE / TITLE, alphabetical
	}
}

// loadOverview builds the :overview rows: open incidents + alerting monitors
// from every active org, worst first. It reuses the per-org incidents/monitors
// caches, so a refresh costs at most two calls per org.
func (a *App) loadOverview(force bool) {
	if a.loading {
		return
	}
	a.loading = true
	entries := a.activeEntries()
	var incR, monR data.Resource
	for _, r := range data.Resources() {
		switch r.Key {
		case "incidents":
			incR = a.tuneResource(r)
		case "monitors":
			monR = a.tuneResource(r)
		}
	}
	go func() {
		var mu sync.Mutex
		var out []data.Row
		var firstErr error
		var wg sync.WaitGroup
		for _, e := range entries {
			wg.Add(1)
			go func(e ctxProvider) {
				defer wg.Done()
				incs, _, _, ierr := e.p.Fetch(context.Background(), incR, "", "", force)
				mons, _, _, merr := e.p.Fetch(context.Background(), monR, "", "", force)
				mu.Lock()
				defer mu.Unlock()
				for _, r := range incs {
					if len(r.Cells) < 6 || strings.EqualFold(r.Cells[2], "resolved") {
						continue
					}
					out = append(out, data.Row{
						ID: r.ID, Ctx: e.name, URL: r.URL,
						Cells: []string{"incident", r.Cells[1] + " " + r.Cells[2], r.Cells[3], r.Cells[5]},
						Raw:   map[string]any{"kind": "incidents"},
					})
				}
				for _, r := range mons {
					if len(r.Cells) < 3 {
						continue
					}
					state := r.Cells[0]
					if state != "Alert" && state != "Warn" && state != "No Data" {
						continue
					}
					out = append(out, data.Row{
						ID: r.ID, Ctx: e.name, URL: r.URL, LogQuery: r.LogQuery, Muted: r.Muted,
						Cells: []string{"monitor", state, r.Cells[2], ""},
						Raw:   map[string]any{"kind": "monitors"},
					})
				}
				if ierr != nil && firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", e.name, ierr)
				}
				if merr != nil && firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", e.name, merr)
				}
			}(e)
		}
		wg.Wait()
		sort.SliceStable(out, func(i, j int) bool { return overviewRank(out[i]) < overviewRank(out[j]) })
		err := firstErr
		a.QueueUpdateDraw(func() {
			a.loading = false
			if a.res.Key != overviewResource.Key {
				return // user navigated away while fetching
			}
			if err != nil {
				a.flash("✗ "+err.Error(), true)
				if data.ErrorIsRateLimit(err) && !a.paused {
					a.paused = true
					a.setHints()
				}
			}
			a.rows = out
			a.fetchedAt = time.Now()
			a.applyFilter()
			if err == nil {
				a.flash(fmt.Sprintf("Overview: %d open across %d org(s)", len(out), len(entries)), false)
			}
		})
	}()
}

// overviewRank orders overview rows worst-first: incidents by severity, then
// monitors Alert > Warn > No Data.
func overviewRank(r data.Row) int {
	status := ""
	if len(r.Cells) > 1 {
		status = r.Cells[1]
	}
	switch {
	case strings.HasPrefix(status, "SEV-1"):
		return 0
	case strings.HasPrefix(status, "SEV-2"):
		return 1
	case strings.HasPrefix(status, "SEV-3"):
		return 2
	case strings.HasPrefix(status, "SEV-"):
		return 3
	case status == "Alert":
		return 10
	case status == "Warn":
		return 11
	}
	return 12
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
	if a.spanning() {
		// Display-only CTX column, first, when >1 org is active. The sentinel
		// index -1 reads Row.Ctx at render time — Row.Cells are never touched,
		// so sorting, quick filters and positional reads are unaffected.
		names = append([]string{"CTX"}, names...)
		cidx = append([]int{-1}, cidx...)
	}
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
		// Active orgs in :ctx are a marked set — tint the whole row so a
		// multi-selection reads at a glance (the cursor bar stays distinct:
		// tview's selected style overrides cell styles on the selected row).
		marked := a.res.Key == ctxResource.Key && len(r.Cells) > 0 && r.Cells[0] == "active"
		for c, ci := range cidx {
			val := ""
			switch {
			case ci == -1:
				val = r.Ctx
			case ci < len(r.Cells):
				val = r.Cells[ci]
			}
			if len(val) > 200 {
				val = val[:197] + "…"
			}
			cell := tview.NewTableCell(tview.Escape(val)).
				SetTextColor(color).
				SetExpansion(expansion(names[c]))
			if marked {
				cell.SetBackgroundColor(a.theme.MarkBg).SetAttributes(tcell.AttrBold)
			}
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
	case "overview":
		status := ""
		if len(r.Cells) > 1 {
			status = strings.ToLower(r.Cells[1])
		}
		switch {
		case strings.Contains(status, "sev-1"), strings.Contains(status, "active"), strings.Contains(status, "alert"):
			return tcell.ColorRed
		case strings.Contains(status, "warn"), strings.Contains(status, "stable"), strings.Contains(status, "sev-2"):
			return tcell.ColorYellow
		case strings.Contains(status, "no data"):
			return tcell.ColorGray
		}
		return tcell.ColorLightGray
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
	if entries := a.activeEntries(); len(entries) > 1 {
		// Several orgs active: one budget line per org, prefixed by context.
		var lines []string
		for _, e := range entries {
			if b := formatBudget(e.p.Budget()); b != "-" {
				lines = append(lines, tview.Escape(e.name)+": "+b)
			}
		}
		if len(lines) > 0 {
			budget = strings.Join(lines, "\n")
		}
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
	// Overview rows resolve to their underlying resource kind so the detail
	// (incident People header, monitor sparkline) matches the real object.
	detKey := a.res.Key
	if detKey == overviewResource.Key {
		if raw, ok := r.Raw.(map[string]any); ok {
			if k, _ := raw["kind"].(string); k != "" {
				detKey = k
			}
		}
	}
	res := a.res
	go func() {
		full, err := a.providerFor(r).FetchDetail(context.Background(), detKey, r.ID)
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
		switch detKey {
		case "slos":
			if d, ok := full.(*data.SLODetail); ok {
				body = sloDetailBody(d)
			} else {
				body = jsonIndent(full)
			}
		case "synthetics":
			if d, ok := full.(*data.SynthDetail); ok {
				body = synthDetailBody(r, d)
			} else {
				body = jsonIndent(full)
			}
		case "incidents":
			// The war room: structured summary, people, impacts and to-dos in
			// one screen, with the raw object at the bottom for completeness.
			if d, ok := full.(*data.IncidentDetail); ok {
				prov := a.providerFor(r)
				todos, tErr := prov.IncidentTodos(context.Background(), r.ID)
				impacts, iErr := prov.IncidentImpacts(context.Background(), r.ID)
				if tErr != nil {
					slog.Warn("war room: to-dos unavailable", "id", r.ID, "err", tErr)
				}
				if iErr != nil {
					slog.Warn("war room: impacts unavailable", "id", r.ID, "err", iErr)
				}
				body = warRoomBody(r.ID, d, impacts, todos) + jsonIndent(d.Incident)
			} else {
				body = jsonIndent(full)
			}
		case "monitors":
			// Structured header + the evaluated metric sparkline — the data
			// behind the alert, so the detail answers "why is it firing?".
			if d, ok := full.(*data.MonitorDetail); ok {
				body = monitorDetailBody(d) + jsonIndent(d.Monitor)
			} else {
				body = jsonIndent(full)
			}
			if ms, mErr := a.providerFor(r).MonitorMetric(context.Background(), r.ID); mErr == nil {
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

// warRoomBody renders the incident war room: identity line, summary, people,
// impacts, to-dos and non-empty fields — plain text (the detail view has
// dynamic colours off), sections in triage order.
func warRoomBody(id string, d *data.IncidentDetail, impacts []string, todos []data.Todo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s · %s · %s ━━\n", id, d.Severity, d.State)
	if d.Title != "" {
		b.WriteString("  " + d.Title + "\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %-13s%s\n", "created:", d.Created)
	impact := "no"
	if d.CustomerImpacted {
		impact = "YES"
		if d.ImpactScope != "" {
			impact += " · " + d.ImpactScope
		}
	}
	fmt.Fprintf(&b, "  %-13s%s\n\n", "customer:", impact)

	b.WriteString(incidentPeopleHeader(d.People))

	b.WriteString("── impacts ──\n")
	if len(impacts) == 0 {
		b.WriteString("  (none declared)\n")
	}
	for _, im := range impacts {
		b.WriteString("  • " + im + "\n")
	}
	b.WriteString("\n── to-dos ──\n")
	if len(todos) == 0 {
		b.WriteString("  (none — press esc, then T for the to-do panel)\n")
	}
	for _, t := range todos {
		mark := "[ ]"
		if t.Completed {
			mark = "[x]"
		}
		line := "  " + mark + " " + t.Content
		if len(t.Assignees) > 0 {
			line += "   @" + strings.Join(t.Assignees, " @")
		}
		b.WriteString(line + "\n")
	}
	if len(d.Fields) > 0 {
		b.WriteString("\n── fields ──\n")
		keys := make([]string, 0, len(d.Fields))
		for k := range d.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-13s%s\n", k+":", d.Fields[k])
		}
	}
	b.WriteString("\n── raw ──\n")
	return b.String()
}

// monitorDetailBody renders the structured monitor header: identity, config
// and the alert message (runbook links live there), above the raw object.
func monitorDetailBody(d *data.MonitorDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s ━━\n\n", d.Name)
	fmt.Fprintf(&b, "  %-10s%s", "state:", d.State)
	if d.Priority != "" {
		fmt.Fprintf(&b, " · %s", d.Priority)
	}
	fmt.Fprintf(&b, " · %s\n", d.Type)
	if d.Query != "" {
		fmt.Fprintf(&b, "  %-10s%s\n", "query:", d.Query)
	}
	if len(d.Tags) > 0 {
		fmt.Fprintf(&b, "  %-10s%s\n", "tags:", strings.Join(d.Tags, " "))
	}
	if d.Message != "" {
		b.WriteString("\n── message ──\n")
		for _, line := range strings.Split(strings.TrimSpace(d.Message), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n── raw ──\n")
	return b.String()
}

// synthDetailBody renders a synthetic test's latest results: pass rate on
// top, then one line per recent run (when, from where, PASS/FAIL).
func synthDetailBody(r data.Row, d *data.SynthDetail) string {
	var b strings.Builder
	name := d.Name
	if name == "" && len(r.Cells) > 1 {
		name = r.Cells[1]
	}
	fmt.Fprintf(&b, "━━ %s ━━\n\n", name)
	if d.Note != "" {
		b.WriteString("  " + d.Note + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %-12s%.1f%% over the last %d runs\n\n", "pass rate:", d.PassRatePct, len(d.Results))
	b.WriteString("── latest results ──\n")
	for _, res := range d.Results {
		verdict := "PASS"
		if !res.Passed {
			verdict = "FAIL"
		}
		fmt.Fprintf(&b, "  %s  %-22s %s\n", res.CheckTime, res.Location, verdict)
	}
	return b.String()
}

// sloDetailBody renders the structured SLO detail: config, attainment, error
// budget, burn rate and the budget burndown sparkline (plain text — the
// detail view has dynamic colours off).
func sloDetailBody(d *data.SLODetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "━━ %s ━━\n\n", d.Name)
	fmt.Fprintf(&b, "  %-13s%s\n", "type:", d.Type)
	fmt.Fprintf(&b, "  %-13s%.2f%% over %dd\n", "target:", d.TargetPct, d.TimeframeDays)
	fmt.Fprintf(&b, "  %-13s%.3f%%\n", "attainment:", d.AttainmentPct)
	fmt.Fprintf(&b, "  %-13s%.1f%%\n", "budget left:", d.BudgetRemainingPct)
	if d.BurnRate > 0 {
		verdict := "sustainable"
		if d.BurnRate > 1 {
			verdict = "ON TRACK TO BREACH"
		}
		fmt.Fprintf(&b, "  %-13s%.2fx (%s)\n", "burn rate:", d.BurnRate, verdict)
	}
	b.WriteString("\n── error-budget burndown ──\n")
	if len(d.Burndown) > 1 {
		fmt.Fprintf(&b, "  %s  now %.1f%%\n", data.Sparkline(d.Burndown), d.Burndown[len(d.Burndown)-1])
		fmt.Fprintf(&b, "  window: last %dd, oldest → newest\n", d.TimeframeDays)
	} else if d.Note != "" {
		b.WriteString("  " + d.Note + "\n")
	} else {
		b.WriteString("  (no series)\n")
	}
	return b.String()
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
		view, err := a.providerFor(r).Dashboard(context.Background(), r.ID)
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
