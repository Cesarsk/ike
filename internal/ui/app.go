package ui

import (
	"context"
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

	"github.com/Cesarsk/ike/internal/data"
)

type promptMode int

const (
	promptNone promptMode = iota
	promptCmd
	promptFilter
	promptSaveQuery  // naming the current query for the 'Q' picker
	promptSettings   // typing a TTL/columns value in the :settings editor
	promptTodo       // typing an incident to-do's content ('T')
	promptCostFilter // client-side product/org filter on the :cost panel
	promptPageTitle  // typing an On-Call page title ('p' on :oncall)
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
	// :cost — Datadog spend panel: a header (totals, anomaly count, trend)
	// over a selectable breakdown table whose rows drill into a per-product
	// history page, plus the local range / month / sub-org / filter knobs
	// the key handler drives.
	costFlex     *tview.Flex
	costHead     *tview.TextView
	costTbl      *tview.Table
	costProd     *tview.TextView // per-product drill-down ("costprod" page)
	costRows     []costLineDelta // table data row i ↔ costRows[i] (header is row 0)
	costView     *data.CostView
	costMonths   int    // fetch range: 1, 3, 6 or 12 months
	costSel      int    // selected month index into costView.Months
	costSubOrg   bool   // "sub-org" API view instead of "summary"
	costOrgFocus string // sub-org focus ("" = all; f cycles)
	costFilter   string // client-side substring filter over org/product
	// :oncall drill-in panel (enter on a team): who is on call now + the
	// escalation ladder for onCallTeam, fetched on demand.
	onCall       *tview.TextView
	onCallTeam   data.Row
	onCallDetail *data.OnCallDetail // stored so paging can re-render without a re-fetch
	onCallPageID string             // id of a page raised from the panel ("" = none)
	// :teams drill-in panel (enter on a team): the team's members + roles.
	teamMembers *tview.TextView
	teamRow     data.Row
	// :notebooks reading panel (enter on a notebook): its rendered cells.
	notebook    *tview.TextView
	notebookRow data.Row
	splash      *tview.TextView // startup logo, auto-dismissed
	// Log surrounding-context panel (x in :logs): a caption + a selectable
	// table of the ±window, so lines can be navigated and expanded.
	logCtxFlex *tview.Flex
	logCtxCap  *tview.TextView
	logCtxTbl  *tview.Table
	logCtxRows []data.Row      // table data row i ↔ logCtxRows[i] (header is table row 0)
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
	// marks is the bulk-selection set on a normal table (space toggles a row),
	// keyed by rowKey (org-safe). Cleared on view switch / esc / after a bulk
	// action. Drives the row tint and the m/r/x fan-out writes.
	marks map[string]bool
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
	for _, tv := range []*tview.TextView{a.detail, a.dash, a.trace, a.patterns, a.costProd, a.onCall, a.teamMembers, a.notebook} {
		tv.SetBorderColor(a.theme.Border)
		tv.SetTitleColor(a.theme.Title)
	}
	if a.logCtxFlex != nil {
		a.logCtxFlex.SetBorderColor(a.theme.Border)
		a.logCtxFlex.SetTitleColor(a.theme.Title)
		a.logCtxTbl.SetSelectedStyle(sel)
	}
	if a.costFlex != nil {
		a.costFlex.SetBorderColor(a.theme.Border)
		a.costFlex.SetTitleColor(a.theme.Title)
		a.costTbl.SetSelectedStyle(sel)
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
		switch {
		case a.promptM == promptFilter && !a.res.ServerQuery:
			a.filter = text
			a.applyFilter()
		case a.promptM == promptCostFilter:
			a.costFilter = text
			a.renderCostPage()
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

	a.costHead = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.costTbl = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false)
	a.costTbl.SetSelectedFunc(func(int, int) { a.openCostProduct() })
	a.costFlex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.costHead, 6, 0, false).
		AddItem(a.costTbl, 0, 1, true)
	a.costFlex.SetBorder(true).SetTitle(" Cost ")
	a.costProd = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.costProd.SetBorder(true)

	a.onCall = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.onCall.SetBorder(true)

	a.teamMembers = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.teamMembers.SetBorder(true)

	a.notebook = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	a.notebook.SetBorder(true)

	a.logCtxCap = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.logCtxTbl = tview.NewTable().SetFixed(1, 0).SetSelectable(true, false)
	a.logCtxTbl.SetSelectedFunc(func(int, int) { a.expandLogCtx() })
	a.logCtxFlex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.logCtxCap, 2, 0, false).
		AddItem(a.logCtxTbl, 0, 1, true)
	a.logCtxFlex.SetBorder(true).SetTitle(" Log context ")

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
		AddPage("cost", a.costFlex, true, false).
		AddPage("costprod", a.costProd, true, false).
		AddPage("oncall", a.onCall, true, false).
		AddPage("teammembers", a.teamMembers, true, false).
		AddPage("notebook", a.notebook, true, false).
		AddPage("patterns", a.patterns, true, false).
		AddPage("logcontext", a.logCtxFlex, true, false).
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
	case "logcontext":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Rune() == 't':
			if r, ok := a.logCtxSelected(); ok {
				a.drillToTrace(r) // jump to the selected line's trace
			}
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
	case "cost":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			if a.costFilter != "" {
				a.costFilter = ""
				a.renderCostPage()
				return nil
			}
			a.back()
			return nil
		case ev.Key() == tcell.KeyCtrlR:
			a.showCost() // re-fetch
			return nil
		case ev.Rune() == '1':
			a.setCostRange(1)
			return nil
		case ev.Rune() == '3':
			a.setCostRange(3)
			return nil
		case ev.Rune() == '6':
			a.setCostRange(6)
			return nil
		case ev.Rune() == 'y':
			a.setCostRange(12)
			return nil
		case ev.Rune() == 's':
			a.costSubOrg = !a.costSubOrg
			a.showCost()
			return nil
		case ev.Key() == tcell.KeyEnter:
			a.openCostProduct()
			return nil
		case ev.Rune() == 'f':
			a.cycleCostOrg()
			return nil
		case ev.Rune() == 'o':
			a.openCostURL()
			return nil
		case ev.Rune() == '[':
			a.moveCostMonth(-1) // newer
			return nil
		case ev.Rune() == ']':
			a.moveCostMonth(1) // older
			return nil
		case ev.Rune() == '/':
			a.openPrompt(promptCostFilter)
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
	case "costprod":
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
	case "oncall":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Key() == tcell.KeyCtrlR:
			a.showTeamOnCall(a.onCallTeam) // re-fetch
			return nil
		case ev.Rune() == 'o':
			a.openOnCallURL()
			return nil
		case ev.Rune() == 'p':
			a.startPageTeam() // page this team (confirm-gated)
			return nil
		case ev.Rune() == 'a':
			a.pageAction("acknowledge") // no-op unless a page is in flight
			return nil
		case ev.Rune() == 'e':
			a.pageAction("escalate")
			return nil
		case ev.Rune() == 'r':
			a.pageAction("resolve")
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
	case "teammembers":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Key() == tcell.KeyCtrlR:
			a.showTeamMembers(a.teamRow) // re-fetch
			return nil
		case ev.Rune() == 'o':
			a.openTeamURL()
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
	case "notebook":
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			a.back()
			return nil
		case ev.Key() == tcell.KeyCtrlR:
			a.showNotebook(a.notebookRow) // re-fetch
			return nil
		case ev.Rune() == 'o':
			a.openNotebookURL()
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
		a.toggleMark() // bulk-selection mark on a normal table
		return nil
	case 'm':
		if a.res.Key == "monitors" {
			if len(a.marks) > 0 {
				a.bulkMute()
			} else if row, ok := a.selectedRow(); ok {
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
			if len(a.marks) > 0 {
				a.bulkResolveIncidents()
			} else if row, ok := a.selectedRow(); ok {
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
			if len(a.marks) > 0 {
				a.bulkCancelDowntimes()
			} else if row, ok := a.selectedRow(); ok {
				a.confirmCancelDowntime(row)
			}
			return nil
		}
		if a.res.Key == "logs" {
			if row, ok := a.selectedRow(); ok {
				a.showLogContext(row) // conteXt: ±window around this line
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
	case m == promptCostFilter:
		a.prompt.SetLabel(" /")
		prefill = a.costFilter // edit the active filter, don't retype
	case m == promptPageTitle:
		a.prompt.SetLabel(" page title> ")
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
	case "cost":
		a.SetFocus(a.costTbl)
	case "oncall":
		a.SetFocus(a.onCall)
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
		if mode == promptCostFilter {
			a.costFilter = ""
			a.renderCostPage()
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
	case promptCostFilter:
		a.costFilter = text
		a.renderCostPage()
	case promptPageTitle:
		a.confirmPageTeam(text)
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
	if cmd == "menu" || cmd == "commands" || cmd == "cmds" || cmd == "aliases" {
		a.showMenu()
		return
	}
	if cmd == "cost" || cmd == "costs" || cmd == "billing" {
		a.showCost()
		return
	}
	if res, ok := data.ResourceByAlias(cmd); ok {
		a.switchResource(res)
		a.persistSession() // remember this view for the next session
		return
	}
	a.flash(fmt.Sprintf("unknown command %q — :menu lists every command", cmd), true)
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
	names = append(names, "ctx", "overview", "cost", "menu", "settings", "help", "manual", "quit")
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
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

// logContextWindowSecs is the ±half-width of the surrounding-context lens.
const logContextWindowSecs = 300 // ±5 minutes

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
	a.marks = nil // a fresh view starts with no bulk selection
}

// back implements k9s's esc semantics (Browser.resetCmd): clear any active
// filter, then pop the navigation stack to the previous view. At the root
// with no filter, esc is a no-op.
func (a *App) back() {
	// k9s esc semantics: an active filter or bulk selection is cleared first;
	// only a second esc (nothing left to clear) pops the navigation stack.
	if a.page == "table" && (a.filter != "" || a.colFilterVal != "" || len(a.marks) > 0) {
		a.filter = ""
		a.colFilterCol, a.colFilterVal = -1, ""
		a.marks = nil
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
	case "logcontext":
		a.showPage("logcontext") // pane still holds the rendered context
	case "cost":
		a.showPage("cost") // pane still holds the rendered breakdown
	case "costprod":
		a.showPage("costprod")
	case "oncall":
		a.showPage("oncall") // pane still holds the rendered on-call
	case "teammembers":
		a.showPage("teammembers")
	case "notebook":
		a.showPage("notebook")
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
	case "cost":
		a.SetFocus(a.costTbl)
	case "costprod":
		a.SetFocus(a.costProd)
	case "oncall":
		a.SetFocus(a.onCall)
	case "teammembers":
		a.SetFocus(a.teamMembers)
	case "notebook":
		a.SetFocus(a.notebook)
	case "patterns":
		a.SetFocus(a.patterns)
	case "logcontext":
		a.SetFocus(a.logCtxTbl)
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
	if a.res.Key == menuResource.Key {
		// The :menu view is the app's own command catalog, not a Provider.
		a.rows = a.menuRows()
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
				// Bound every fetch: without a deadline a stalled provider call
				// never returns, so a.loading never clears and every later view
				// switch becomes a silent no-op — the app looks hung.
				fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				rows, at, cached, err := e.p.Fetch(fctx, res, q, tr, force)
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

// budgetLine matches the provider's "name remaining/limit per Ns" budget
// strings so the header can render compact, colour-coded headroom.
var budgetLine = regexp.MustCompile(`^(\S+)\s+(\d+)/(\d+)\s+per`)

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

// rowKey identifies a row for the bulk-selection set. It includes the origin
// org so marks never collide across spanned orgs.
func rowKey(r data.Row) string { return r.Ctx + "\x00" + r.ID }

// toggleMark adds or removes the selected row from the bulk-selection set.
func (a *App) toggleMark() {
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	if a.marks == nil {
		a.marks = map[string]bool{}
	}
	k := rowKey(r)
	if a.marks[k] {
		delete(a.marks, k)
	} else {
		a.marks[k] = true
	}
	a.render()
}

// markedRows returns the marked rows within the currently loaded set, in
// display order.
func (a *App) markedRows() []data.Row {
	var out []data.Row
	for _, idx := range a.filtered {
		if a.marks[rowKey(a.rows[idx])] {
			out = append(out, a.rows[idx])
		}
	}
	return out
}

// dashGridCols is Datadog's dashboard grid width in layout units; widgets
// pack into terminal rows until their widths sum past it, approximating the
// real 2-D arrangement (a width-12 widget fills a row; two width-6 share one).
const dashGridCols = 12

var tagRe = regexp.MustCompile(`\[[a-zA-Z0-9_,:;.#-]*\]`)

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
