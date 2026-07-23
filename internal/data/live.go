package data

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// Live talks to the real Datadog API for one organization. Credentials are
// injected per instance (resolved from a context's env vars by the caller),
// so several Live providers for different orgs can coexist.
// Live satisfies Provider.
var _ Provider = (*Live)(nil)

type Live struct {
	client *datadog.APIClient
	site   string
	web    string // browser base URL for deep links (may be an org subdomain)
	apiKey string
	appKey string
	token  string // bearer/access token — used instead of the key pair
	// tokenSource supplies a fresh bearer token per request (OAuth contexts:
	// lazy refresh lives behind it). Takes precedence over the static token.
	tokenSource func(context.Context) (string, error)
	mu          sync.Mutex
	limits      map[string]string
}

func newLive(site, webBase string) *Live {
	cfg := datadog.NewConfiguration()
	cfg.SetUnstableOperationEnabled("v2.ListIncidents", true)
	cfg.SetUnstableOperationEnabled("v2.GetIncident", true)
	cfg.SetUnstableOperationEnabled("v2.UpdateIncident", true)
	cfg.SetUnstableOperationEnabled("v2.GetTraceByID", true)
	if webBase == "" {
		webBase = WebBase(site)
	}
	return &Live{
		client: datadog.NewAPIClient(cfg),
		site:   site,
		web:    webBase,
		limits: map[string]string{},
	}
}

// NewLive authenticates with an API key + application key pair. webBase is
// the browser URL for deep links ("" = derive from site); orgs with a custom
// subdomain pass e.g. https://acme-stage.datadoghq.eu.
func NewLive(site, webBase, apiKey, appKey string) *Live {
	l := newLive(site, webBase)
	l.apiKey, l.appKey = apiKey, appKey
	return l
}

// NewLiveToken authenticates with a bearer/access token (OAuth2 access
// token or PAT) via the Authorization header.
func NewLiveToken(site, webBase, token string) *Live {
	l := newLive(site, webBase)
	l.token = token
	return l
}

// NewLiveTokenSource authenticates through a per-request token supplier (the
// OAuth path: the source refreshes lazily and hands back a valid token).
func NewLiveTokenSource(site, webBase string, source func(context.Context) (string, error)) *Live {
	l := newLive(site, webBase)
	l.tokenSource = source
	return l
}

// authCtx attaches this org's credentials and site to a request context.
func (l *Live) authCtx(parent context.Context) context.Context {
	var ctx context.Context
	if l.tokenSource != nil {
		tok, err := l.tokenSource(parent)
		if err != nil {
			// No way to return an error here without rippling through every
			// call site; an empty bearer fails the request with a clear 4xx
			// and the source's message is logged for the debug trail.
			slog.Warn("oauth token unavailable", "err", err)
		}
		ctx = context.WithValue(parent, datadog.ContextAccessToken, tok)
		return context.WithValue(ctx, datadog.ContextServerVariables, map[string]string{
			"site": l.site,
		})
	}
	if l.token != "" {
		ctx = context.WithValue(parent, datadog.ContextAccessToken, l.token)
	} else {
		ctx = context.WithValue(parent, datadog.ContextAPIKeys, map[string]datadog.APIKey{
			"apiKeyAuth": {Key: l.apiKey},
			"appKeyAuth": {Key: l.appKey},
		})
	}
	return context.WithValue(ctx, datadog.ContextServerVariables, map[string]string{
		"site": l.site,
	})
}

func (l *Live) Mode() string { return "live" }
func (l *Live) Site() string { return l.site }

// track records the rate-limit headers Datadog returns on every response,
// so the header widget can show real remaining budget per endpoint family.
func (l *Live) track(resp *http.Response) {
	if resp == nil {
		return
	}
	name := resp.Header.Get("X-Ratelimit-Name")
	if name == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits[name] = fmt.Sprintf("%s %s/%s per %ss",
		name,
		resp.Header.Get("X-Ratelimit-Remaining"),
		resp.Header.Get("X-Ratelimit-Limit"),
		resp.Header.Get("X-Ratelimit-Period"))
}

func (l *Live) Budget() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.limits))
	for _, v := range l.limits {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (l *Live) Fetch(ctx context.Context, key, query, timeRange string) ([]Row, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch key {
	case "monitors":
		return l.monitors(ctx)
	case "incidents":
		return l.incidents(ctx)
	case "slos":
		return l.slos(ctx)
	case "logs":
		return l.logs(ctx, query, timeRange)
	case "dashboards":
		return l.dashboards(ctx)
	case "traces":
		return l.spans(ctx, query, timeRange)
	case "services":
		return l.services(ctx, query, timeRange)
	case "events":
		return l.events(ctx, query, timeRange)
	case "rum":
		return l.rum(ctx, query, timeRange)
	case "synthetics":
		return l.synthetics(ctx)
	case "downtimes":
		return l.downtimes(ctx)
	case "teams":
		return l.teams(ctx)
	case "oncall":
		return l.oncallTeams(ctx)
	case "security":
		return l.securitySignals(ctx, query, timeRange)
	case "notebooks":
		return l.notebooks(ctx)
	case "hosts":
		return l.hosts(ctx)
	case "containers":
		return l.containers(ctx)
	}
	return nil, fmt.Errorf("unknown resource %q", key)
}

// FetchDetail upgrades a list row to the full object where the list
// endpoint returns only a summary. Monitors gain full options/thresholds,
// dashboards their complete widget definition, incidents their full
// attributes. SLO and log rows are already complete → (nil, nil).
func (l *Live) FetchDetail(ctx context.Context, key, id string) (any, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch key {
	case "monitors":
		mid, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("monitor id %q: %w", id, err)
		}
		m, resp, err := datadogV1.NewMonitorsApi(l.client).GetMonitor(ctx, mid,
			*datadogV1.NewGetMonitorOptionalParameters())
		l.track(resp)
		if err != nil {
			return nil, apiErr("monitor detail", err)
		}
		prio := ""
		if p, ok := m.GetPriorityOk(); ok && p != nil {
			prio = fmt.Sprintf("P%d", *p)
		}
		return &MonitorDetail{
			Name: m.GetName(), State: string(m.GetOverallState()),
			Type: string(m.GetType()), Priority: prio,
			Query: m.GetQuery(), Message: m.GetMessage(), Tags: m.GetTags(),
			Monitor: m,
		}, nil
	case "dashboards":
		d, resp, err := datadogV1.NewDashboardsApi(l.client).GetDashboard(ctx, id)
		l.track(resp)
		if err != nil {
			return nil, apiErr("dashboard detail", err)
		}
		return d, nil
	case "incidents":
		// include=users carries the incident's user objects in the response so
		// the People header can resolve commander/created/declared ids to
		// handles in this one call (GetIncident is otherwise bare).
		in, resp, err := datadogV2.NewIncidentsApi(l.client).GetIncident(ctx, id,
			*datadogV2.NewGetIncidentOptionalParameters().
				WithInclude([]datadogV2.IncidentRelatedObject{datadogV2.INCIDENTRELATEDOBJECT_USERS}))
		l.track(resp)
		if err != nil {
			return nil, apiErr("incident detail", err)
		}
		return incidentDetail(in), nil
	case "slos":
		return l.sloStatus(ctx, id)
	case "synthetics":
		return l.synthDetail(ctx, id)
	}
	return nil, nil // the list row is already the full object
}

// MaxDashWidgets bounds how many metric sparklines one dashboard render will
// fetch. The timeseries query API is the tightest budget we spend, so a
// 40-widget dashboard cannot fan out to 40 requests on a single open.
const MaxDashWidgets = 12

// Pagination caps: bounded so one view refresh can never spend more than a
// handful of requests from the org-wide budget. Truncation is logged, never
// silent.
const (
	monitorPageSize = 200
	maxMonitorPages = 10 // 2000 monitors
	sloPageSize     = 1000
	maxSLOPages     = 3 // 3000 SLOs
	incidentPage    = 100
	maxIncidentPage = 3 // 300 incidents
	dashPageSize    = 100
	maxDashPages    = 10 // 1000 dashboards
)

const eventsPageLimit = 100 // one page of events per fetch

const spansPageLimit = 100 // one page of spans per search (bounded budget)

// servicesDefaultEnv is the APM env used when the :services query is unset. The
// service-list endpoint is env-scoped (filter[env]), so an env is always sent.
const servicesDefaultEnv = "prod"

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1" || t == "error"
	}
	return false
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

// ErrRateLimited marks a 429 so the UI can back off (pause auto-refresh)
// instead of hammering the org's shared budget. Detected via ErrorIsRateLimit.
const rateLimitPhrase = "rate limit exceeded"

func apiErr(what string, err error) error {
	if oe, ok := err.(datadog.GenericOpenAPIError); ok && len(oe.Body()) > 0 {
		body := string(oe.Body())
		if len(body) > 200 {
			body = body[:200]
		}
		if is429(err.Error(), body) {
			return fmt.Errorf("%s: %s — Datadog is throttling this org (shared budget); ike auto-pauses auto-refresh, use ctrl-r sparingly", what, rateLimitPhrase)
		}
		return fmt.Errorf("%s: %s — %s", what, err.Error(), body)
	}
	if is429(err.Error(), "") {
		return fmt.Errorf("%s: %s", what, rateLimitPhrase)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func is429(msgs ...string) bool {
	for _, m := range msgs {
		if strings.Contains(m, "429") || strings.Contains(strings.ToLower(m), "too many requests") {
			return true
		}
	}
	return false
}

// ErrorIsRateLimit reports whether an error came from a 429 throttle.
func ErrorIsRateLimit(err error) bool {
	return err != nil && strings.Contains(err.Error(), rateLimitPhrase)
}
