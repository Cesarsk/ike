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

// IncidentStates are the states an incident can be moved to via 'r'.
var IncidentStates = []string{"active", "stable", "resolved"}

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
	// SetIncidentState changes an incident's state (e.g. active → resolved).
	// A write operation; the UI gates it behind a confirmation modal.
	SetIncidentState(ctx context.Context, id, state string) error
	// SetMonitorMute mutes or unmutes a monitor. Implemented as a
	// read-modify-write on the monitor's options so no other option is
	// clobbered. A write operation; UI-gated behind confirmation.
	SetMonitorMute(ctx context.Context, id string, mute bool) error
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
			Columns: []string{"STATE", "NAME", "TYPE", "PRIO", "TAGS"},
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
