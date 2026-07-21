package data

import (
	"context"
	"strings"
	"time"
)

// Row is one entry in a resource table.
type Row struct {
	ID    string
	Cells []string
	Raw   any    // full object, rendered in the detail view
	URL   string // deep link into the Datadog web UI
	// LogQuery is the derived Datadog logs search for the 'l' drill-down
	// (monitors: the log monitor's own query, or service:/env: tags).
	// Empty = no drill-down available for this row.
	LogQuery string
	// Muted reports whether a monitor is currently silenced. It is the
	// authoritative source for the mute/unmute toggle — mute is independent
	// of overall_state, so it cannot be read off the STATE column.
	Muted bool
	// TraceID links a row to a distributed trace: set on log rows (from the
	// injected trace_id attribute) and span rows. Empty = no trace to jump
	// to. Drives the log/span → trace waterfall drill-down ('t').
	TraceID string
	// Ctx names the context (org) this row came from. With several contexts
	// active, views span orgs and every detail fetch, drill-down and write
	// must route to the row's origin org — this field carries that routing.
	// Empty means the current context.
	Ctx string
}

// LogContextView is the surrounding-context lens for one log line: the events
// in a bounded ±window around the anchor, from the same service/host, oldest
// first. One API call, no polling — the cheap, cost-safe half of live tail.
type LogContextView struct {
	AnchorID string        // the selected log's id; rendered highlighted in Rows
	Service  string        // scope of the context query ("" = any)
	Host     string        // scope of the context query ("" = any)
	Window   time.Duration // half-width: rows span [anchor-Window, anchor+Window]
	Rows     []Row         // ascending by timestamp; log-shaped cells
}

// CostView is one org's Datadog spend for the current month: what it has
// accrued so far (Estimated) and the projected end-of-month total, with a
// per-product breakdown. Amounts are in Currency (USD unless the org bills
// otherwise). Lines are sorted highest-cost first.
type CostView struct {
	OrgName   string
	Month     string // "2026-07"
	Currency  string
	Estimated float64
	Projected float64
	Lines     []CostLine
}

// CostLine is one product's slice of the bill (e.g. "infra_hosts").
type CostLine struct {
	Product   string
	Estimated float64
	Projected float64
}

// Resource describes a navigable Datadog resource type.
type Resource struct {
	Key         string
	Title       string
	Aliases     []string
	Columns     []string
	TTL         time.Duration
	AutoRefresh bool
	// ServerQuery: '/' sends the query to the Datadog API (server-side
	// search) instead of filtering the loaded rows client-side.
	ServerQuery  bool
	DefaultQuery string
}

// Widget is one panel of a rendered dashboard: its title, type, primary
// metric query, a fetched sparkline where the query resolved to data, and
// the widget's dashboard layout (x/y/width/height in Datadog grid units)
// so the TUI can approximate the real dashboard arrangement.
type Widget struct {
	Title      string
	Type       string
	Query      string
	Spark      []float64
	Last       float64
	HasData    bool
	Note       string // why there's no sparkline (unsupported widget / query type)
	X, Y, W, H int    // dashboard grid coords; W==0 → unknown (ordered layout)
}

// DashboardView is a dashboard rendered for the terminal: metadata plus a
// flat, in-order list of its widgets (group widgets are flattened).
type DashboardView struct {
	Title     string
	Widgets   []Widget
	Truncated bool // more metric widgets existed than the fetch budget allowed
}

// Span is one span of a distributed trace, with its depth in the span tree
// and offset/duration for waterfall rendering (all times in microseconds
// relative to the trace start).
type Span struct {
	ID         string
	ParentID   string
	Service    string
	Resource   string
	Depth      int
	OffsetUs   int64 // start, relative to the trace's earliest span
	DurationUs int64
	Error      bool
}

// TraceLog is one log line correlated to a trace (by trace_id), across all
// services, for the unified request timeline.
type TraceLog struct {
	Time    time.Time
	Service string
	Status  string
	Message string
}

// TraceView is a reconstructed trace: its spans in tree (DFS) order plus the
// trace's logs from every service in chronological order, so the UI can draw
// a proportional waterfall and a unified request-timeline below it.
type TraceView struct {
	TraceID   string
	Spans     []Span
	TotalUs   int64
	Logs      []TraceLog // all services' logs for this trace, ascending by time
	Truncated bool       // more spans than the fetch cap
}

// User is a Datadog user — the acting user (commander/to-do assignment) and an
// entry in the assignee picker (populated from ListUsers). Name is best-effort
// (may be empty); Handle is the stable identifier shown and used for to-dos.
type User struct {
	ID     string
	Handle string
	Name   string
}

// Todo is one incident action item (to-do), as listed in the to-do panel ('T').
// Completed is derived from the API's nullable "completed" timestamp (set =
// done). Assignees are user handles.
type Todo struct {
	ID        string
	Content   string
	Assignees []string
	Completed bool
}

// IncidentPeople are the humans attached to an incident, resolved to handles
// for the detail view's People header. Responders are best-effort: the API
// exposes them read-only as a distinct object type with no include support, so
// an unresolved responder falls back to its raw id rather than a fake name.
type IncidentPeople struct {
	Commander  string
	DeclaredBy string
	CreatedBy  string
	Responders []string
}

// IncidentDetail is what FetchDetail returns for an incident: a structured
// summary (the war-room header), the resolved People, and the raw incident
// object. One GetIncident call (with include=users) feeds all of it.
type IncidentDetail struct {
	Title            string
	Severity         string
	State            string
	Created          string
	CustomerImpacted bool
	ImpactScope      string
	Fields           map[string]string // non-empty single/multi-value fields
	People           IncidentPeople
	Incident         any
}

// IncidentStates are the states an incident can be moved to via 'r'.
var IncidentStates = []string{"active", "stable", "resolved"}

// IncidentSeverities are the severities an incident can be set to via 'v'.
var IncidentSeverities = []string{"SEV-1", "SEV-2", "SEV-3", "SEV-4", "SEV-5"}

// SLODetail is the structured SLO detail: configuration, live attainment,
// error budget, burn rate and a budget burndown series over the timeframe.
// Note explains a missing burndown (no history for this SLO type).
type SLODetail struct {
	Name               string
	Type               string
	TargetPct          float64
	TimeframeDays      int
	AttainmentPct      float64
	BudgetRemainingPct float64
	// BurnRate is the window-average burn rate: (1-attainment)/(1-target).
	// 1.0 means burning exactly the budget; >1 means on track to breach.
	BurnRate float64
	Burndown []float64 // budget remaining %, oldest → newest
	Note     string
}

// SynthResult is one recent run of a synthetic test: when, from where, and
// whether it passed.
type SynthResult struct {
	CheckTime string
	Location  string
	Passed    bool
}

// SynthDetail is the structured synthetic-test detail: identity plus its most
// recent results and the pass rate over them.
type SynthDetail struct {
	Name        string
	Type        string
	Status      string
	Message     string
	PassRatePct float64
	Results     []SynthResult // newest first
	Note        string        // why results are missing
}

// MonitorDetail is the structured monitor detail: identity, config and the
// alert message, with the raw object kept for completeness.
type MonitorDetail struct {
	Name     string
	State    string
	Type     string
	Priority string
	Query    string
	Message  string
	Tags     []string
	Monitor  any
}

// MetricSeries is a monitor's evaluated metric over a recent window — the
// data behind the alert, for the monitor detail view. Note explains why
// there's no sparkline (non-metric monitor, unparseable query, no data).
type MetricSeries struct {
	Query  string
	Points []float64
	Last   float64
	Note   string
}

// Provider serves rows for resources — live API or built-in demo data.
type Provider interface {
	// Fetch lists rows for a resource. timeRange is a Datadog "from" value
	// (e.g. "now-1h") used only by Logs; other resources ignore it.
	Fetch(ctx context.Context, key, query, timeRange string) ([]Row, error)
	// FetchDetail returns the full object behind a row: list endpoints
	// return summaries (a dashboard listing has no widgets, for example),
	// so the detail view upgrades on demand. Returning (nil, nil) means
	// the list row is already the complete object.
	FetchDetail(ctx context.Context, key, id string) (any, error)
	// Dashboard renders a dashboard's widgets for the TUI, fetching metric
	// sparklines on demand (bounded — the timeseries API is rate-limited).
	Dashboard(ctx context.Context, id string) (*DashboardView, error)
	// Trace reconstructs a distributed trace from its spans (searched by
	// trace_id) into a tree for waterfall rendering. Bounded/on-demand.
	Trace(ctx context.Context, traceID string) (*TraceView, error)
	// LogContext returns the log events in a ±windowSecs window around the
	// anchor row, scoped to its service/host, oldest first. One search call,
	// no polling. windowSecs<=0 uses a default.
	LogContext(ctx context.Context, anchor Row, windowSecs int) (*LogContextView, error)
	// Cost returns this org's Datadog spend for the current month (estimated
	// so far + projected end-of-month), broken down by product. Read-only,
	// heavily rate-limited and admin-scoped — a non-privileged user gets a
	// permission error, which the UI surfaces as "needs usage_read".
	Cost(ctx context.Context) (*CostView, error)
	// MonitorMetric evaluates a monitor's metric query over a recent window
	// so the detail view can show the data behind the alert. On-demand.
	MonitorMetric(ctx context.Context, id string) (*MetricSeries, error)
	// SetIncidentField changes a single-value incident field (e.g. "state"
	// → resolved, "severity" → SEV-2). A write operation; the UI gates it
	// behind a confirmation modal.
	SetIncidentField(ctx context.Context, id, field, value string) error
	// SetMonitorMute mutes or unmutes a monitor. Implemented as a
	// read-modify-write on the monitor's options so no other option is
	// clobbered. A write operation; UI-gated behind confirmation.
	SetMonitorMute(ctx context.Context, id string, mute bool) error
	// CancelDowntime cancels a scheduled/active downtime. Write; UI-gated.
	CancelDowntime(ctx context.Context, id string) error
	// CurrentUser returns the acting user (for commander/to-do assignment).
	CurrentUser(ctx context.Context) (User, error)
	// SetIncidentCommander assigns an incident's commander to a user. A write;
	// UI-gated behind confirmation.
	SetIncidentCommander(ctx context.Context, incidentID, userID string) error
	// AddIncidentTodo adds a to-do (action item) to an incident, assigned to
	// the given user handle. A write; UI-gated (the content prompt).
	AddIncidentTodo(ctx context.Context, incidentID, content, assigneeHandle string) error
	// ListUsers searches active org users (server-side filter on
	// name/email/handle); empty query returns the first page. Backs the
	// commander/to-do assignee picker. Bounded to one page.
	ListUsers(ctx context.Context, query string) ([]User, error)
	// IncidentTodos lists an incident's to-dos for the to-do panel. On-demand.
	IncidentTodos(ctx context.Context, incidentID string) ([]Todo, error)
	// SetIncidentTodoCompleted marks a to-do done/undone. Content and assignees
	// are carried on the Todo so the PATCH doesn't clobber them. A write.
	SetIncidentTodoCompleted(ctx context.Context, incidentID string, todo Todo, done bool) error
	// DeleteIncidentTodo removes a to-do from an incident. A write; UI-gated.
	DeleteIncidentTodo(ctx context.Context, incidentID, todoID string) error
	// IncidentImpacts lists an incident's declared impacts (description +
	// type), for the war-room detail. On-demand.
	IncidentImpacts(ctx context.Context, incidentID string) ([]string, error)
	// Budget reports the last-seen API rate-limit state, one line per
	// endpoint family (from X-RateLimit-* response headers).
	Budget() []string
	Mode() string // "live" or "demo"
	Site() string
}

// Registry of resources the POC knows how to render.
func Resources() []Resource {
	return []Resource{
		{
			Key: "monitors", Title: "Monitors",
			Aliases: []string{"monitors", "monitor", "mon", "m"},
			Columns: []string{"STATE", "MUTED", "NAME", "TYPE", "PRIO", "TAGS"},
			TTL:     30 * time.Second, AutoRefresh: true,
		},
		{
			Key: "incidents", Title: "Incidents",
			Aliases: []string{"incidents", "incident", "inc", "i"},
			Columns: []string{"ID", "SEV", "STATE", "TITLE", "IMPACT", "CREATED"},
			TTL:     60 * time.Second, AutoRefresh: true,
		},
		{
			Key: "slos", Title: "SLOs",
			Aliases: []string{"slos", "slo", "s"},
			Columns: []string{"NAME", "TYPE", "TARGET", "TIMEFRAME", "TAGS"},
			TTL:     5 * time.Minute,
		},
		{
			Key: "logs", Title: "Logs",
			Aliases: []string{"logs", "log", "l"},
			Columns: []string{"TIME", "STATUS", "SERVICE", "HOST", "MESSAGE"},
			TTL:     60 * time.Second, ServerQuery: true, DefaultQuery: "*",
		},
		{
			Key: "dashboards", Title: "Dashboards",
			Aliases: []string{"dashboards", "dashboard", "dash", "d"},
			Columns: []string{"TITLE", "LAYOUT", "AUTHOR", "MODIFIED"},
			TTL:     10 * time.Minute,
		},
		{
			Key: "traces", Title: "Traces",
			Aliases: []string{"traces", "trace", "tr", "apm", "spans"},
			Columns: []string{"TIME", "SERVICE", "RESOURCE", "DURATION", "ERR", "TRACE_ID"},
			TTL:     60 * time.Second, ServerQuery: true, DefaultQuery: "*",
		},
		{
			// The '/' query is the APM env filter (the service-list endpoint is
			// env-scoped), not a span query — default "prod", override with /.
			Key: "services", Title: "Services",
			Aliases: []string{"services", "service", "svc"},
			Columns: []string{"SERVICE"},
			TTL:     60 * time.Second, ServerQuery: true, DefaultQuery: "prod",
		},
		{
			Key: "synthetics", Title: "Synthetics",
			Aliases: []string{"synthetics", "synthetic", "syn"},
			Columns: []string{"STATUS", "NAME", "TYPE", "LOCATIONS", "TAGS"},
			TTL:     5 * time.Minute,
		},
		{
			// RUM events (views, actions, errors, sessions) — '/' is a RUM
			// search query, digits 1-5 set the time window like logs.
			Key: "rum", Title: "RUM",
			Aliases: []string{"rum", "browser"},
			Columns: []string{"TIME", "TYPE", "APPLICATION", "SERVICE", "DETAIL"},
			TTL:     60 * time.Second, ServerQuery: true, DefaultQuery: "*",
		},
		{
			Key: "events", Title: "Events",
			Aliases: []string{"events", "event", "ev"},
			Columns: []string{"TIME", "TYPE", "SOURCE", "TITLE", "TAGS"},
			TTL:     60 * time.Second, AutoRefresh: true, ServerQuery: true, DefaultQuery: "*",
		},
		{
			Key: "downtimes", Title: "Downtimes",
			Aliases: []string{"downtimes", "downtime", "dt", "mutes"},
			Columns: []string{"STATUS", "SCOPE", "MESSAGE", "CREATED"},
			TTL:     60 * time.Second,
		},
	}
}

// ResourceByAlias resolves a command-mode alias like "mon" to a resource.
func ResourceByAlias(s string) (Resource, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, r := range Resources() {
		for _, a := range r.Aliases {
			if a == s {
				return r, true
			}
		}
	}
	return Resource{}, false
}

// WebBase returns the browser base URL for a Datadog site.
func WebBase(site string) string {
	if site == "datadoghq.com" || site == "datadoghq.eu" {
		return "https://app." + site
	}
	return "https://" + site
}
