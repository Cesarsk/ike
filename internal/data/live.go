package data

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	}
	return nil, fmt.Errorf("unknown resource %q", key)
}

// rum lists RUM events (views, actions, errors) via the RUM search API — one
// bounded page, newest first, '/' query passed server-side.
func (l *Live) rum(ctx context.Context, query, timeRange string) ([]Row, error) {
	secs := 900
	if s, ok := rangeSeconds(timeRange); ok {
		secs = s
	}
	from := time.Now().Add(-time.Duration(secs) * time.Second)
	to := time.Now()
	opts := datadogV2.NewListRUMEventsOptionalParameters().
		WithFilterFrom(from).WithFilterTo(to).
		WithSort(datadogV2.RUMSORT_TIMESTAMP_DESCENDING).
		WithPageLimit(100)
	if query != "" && query != "*" {
		opts = opts.WithFilterQuery(query)
	}
	resp, httpresp, err := datadogV2.NewRUMApi(l.client).ListRUMEvents(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("rum search", err)
	}
	events := resp.GetData()
	rows := make([]Row, 0, len(events))
	for _, ev := range events {
		attrs := ev.GetAttributes()
		inner := attrs.GetAttributes()
		typ := rumStr(inner, "type")
		app := rumStr(rumMap(inner, "application"), "name")
		if app == "" {
			app = rumStr(rumMap(inner, "application"), "id")
		}
		detail := rumStr(rumMap(inner, "view"), "url_path")
		if detail == "" {
			detail = rumStr(rumMap(rumMap(inner, "error"), ""), "")
		}
		if detail == "" {
			detail = rumStr(rumMap(inner, "error"), "message")
		}
		if detail == "" {
			detail = rumStr(rumMap(rumMap(inner, "action"), "target"), "name")
		}
		ts := attrs.GetTimestamp()
		rows = append(rows, Row{
			ID:  ev.GetId(),
			Ctx: "",
			Cells: []string{
				ts.Local().Format("15:04:05"), typ, app, attrs.GetService(), detail,
			},
			Raw: map[string]any{
				"id": ev.GetId(), "timestamp": ts.Format(time.RFC3339),
				"type": typ, "application": app, "service": attrs.GetService(),
				"detail": detail, "tags": attrs.GetTags(),
			},
			URL: l.web + "/rum/explorer",
		})
	}
	slog.Debug("rum search", "rows", len(rows), "query", query)
	return rows, nil
}

// rumMap digs a nested map out of a RUM event's free-form attributes.
func rumMap(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

// rumStr digs a string out of a RUM event's free-form attributes.
func rumStr(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
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

// synthetics lists the org's synthetic tests — one call, inventory only
// (name, type, live/paused, locations, tags). Pass/fail is fetched per test
// on enter, and failing synthetics already alert through :monitors.
func (l *Live) synthetics(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV1.NewSyntheticsApi(l.client).ListTests(ctx,
		*datadogV1.NewListTestsOptionalParameters())
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("synthetics list", err)
	}
	tests := resp.GetTests()
	rows := make([]Row, 0, len(tests))
	for _, t := range tests {
		rows = append(rows, Row{
			ID: t.GetPublicId(),
			Cells: []string{
				string(t.GetStatus()), t.GetName(), string(t.GetType()),
				strings.Join(t.GetLocations(), ","), strings.Join(t.GetTags(), ","),
			},
			// The test type drives which latest-results endpoint the detail
			// uses; carried in Raw so the row stays self-contained.
			Raw: map[string]any{
				"public_id": t.GetPublicId(), "name": t.GetName(),
				"type": string(t.GetType()), "status": string(t.GetStatus()),
			},
			URL: l.web + "/synthetics/details/" + t.GetPublicId(),
		})
	}
	return rows, nil
}

// synthDetail fetches a synthetic test's most recent results (API or browser
// endpoint by test type — resolved with one ListTests-independent probe: try
// API first, fall back to browser).
func (l *Live) synthDetail(ctx context.Context, publicID string) (any, error) {
	api := datadogV1.NewSyntheticsApi(l.client)
	out := &SynthDetail{}
	if r, httpresp, err := api.GetAPITestLatestResults(ctx, publicID,
		*datadogV1.NewGetAPITestLatestResultsOptionalParameters()); err == nil {
		l.track(httpresp)
		for _, res := range r.GetResults() {
			rr := res.GetResult()
			out.Results = append(out.Results, SynthResult{
				CheckTime: time.UnixMilli(int64(res.GetCheckTime())).Format(time.RFC3339),
				Location:  res.GetProbeDc(),
				Passed:    rr.GetPassed(),
			})
		}
	} else if r, httpresp, err := api.GetBrowserTestLatestResults(ctx, publicID,
		*datadogV1.NewGetBrowserTestLatestResultsOptionalParameters()); err == nil {
		l.track(httpresp)
		for _, res := range r.GetResults() {
			// Browser results carry no passed flag; zero errors = a pass.
			rr := res.GetResult()
			out.Results = append(out.Results, SynthResult{
				CheckTime: time.UnixMilli(int64(res.GetCheckTime())).Format(time.RFC3339),
				Location:  res.GetProbeDc(),
				Passed:    rr.GetErrorCount() == 0,
			})
		}
	} else {
		out.Note = "latest results unavailable: " + err.Error()
		return out, nil
	}
	passed := 0
	for _, r := range out.Results {
		if r.Passed {
			passed++
		}
	}
	if n := len(out.Results); n > 0 {
		out.PassRatePct = float64(passed) / float64(n) * 100
	} else {
		out.Note = "no recent results"
	}
	return out, nil
}

// sloStatus fetches an SLO's recent history and computes its live attainment
// and error budget — the numbers the list can't show. One API call per open
// (bounded), so it's a detail action, never a per-row list fetch.
func (l *Live) sloStatus(ctx context.Context, id string) (any, error) {
	api := datadogV1.NewServiceLevelObjectivesApi(l.client)
	slo, resp, err := api.GetSLO(ctx, id, *datadogV1.NewGetSLOOptionalParameters())
	l.track(resp)
	if err != nil {
		return nil, apiErr("slo detail", err)
	}
	data := slo.GetData()
	// Window: the first threshold's timeframe (7d/30d/90d), default 30d.
	days := 30
	var target float64
	if th := data.GetThresholds(); len(th) > 0 {
		target = th[0].GetTarget()
		switch th[0].GetTimeframe() {
		case datadogV1.SLOTIMEFRAME_SEVEN_DAYS:
			days = 7
		case datadogV1.SLOTIMEFRAME_NINETY_DAYS:
			days = 90
		}
	}
	to := time.Now().Unix()
	from := to - int64(days*86400)
	hist, hresp, err := api.GetSLOHistory(ctx, id, from, to,
		*datadogV1.NewGetSLOHistoryOptionalParameters())
	l.track(hresp)
	out := &SLODetail{
		Name: data.GetName(), Type: string(data.GetType()),
		TargetPct: target, TimeframeDays: days,
	}
	if err != nil {
		// Config still worth showing even if history is unavailable.
		out.Note = "history unavailable: " + err.Error()
		return out, nil
	}
	hd := hist.GetData()
	overall := hd.GetOverall()
	attained := overall.GetSliValue()
	out.AttainmentPct = attained
	out.Burndown = sloBurndown(hd, target)
	if len(out.Burndown) == 0 {
		out.Note = "no history series for this SLO type — burndown unavailable"
	}
	if target > 0 && target < 100 {
		out.BurnRate = (100 - attained) / (100 - target)
	}
	if target > 0 {
		// Error budget consumed = (target-attained)/(100-target); >100% = breached.
		if attained >= target {
			out.BudgetRemainingPct = 100.0
		} else {
			consumed := (target - attained) / (100 - target) * 100
			if consumed > 100 {
				consumed = 100
			}
			out.BudgetRemainingPct = 100 - consumed
		}
	}
	return out, nil
}

// sloBurndown derives the error-budget-remaining series (oldest → newest) from
// an SLO's history: the overall SLI history where present (monitor/time-slice
// SLOs), else the cumulative numerator/denominator ratio (metric SLOs).
func sloBurndown(hd datadogV1.SLOHistoryResponseData, target float64) []float64 {
	if target <= 0 || target >= 100 {
		return nil
	}
	remaining := func(sli float64) float64 {
		if sli >= target {
			return 100
		}
		consumed := (target - sli) / (100 - target) * 100
		if consumed > 100 {
			consumed = 100
		}
		return 100 - consumed
	}
	overall := hd.GetOverall()
	if h := overall.GetHistory(); len(h) > 1 {
		// Cumulative average of the instantaneous SLI approximates attainment
		// over the window so far — the burndown of the budget.
		var sum float64
		out := make([]float64, 0, len(h))
		for i, p := range h {
			if len(p) < 2 {
				continue
			}
			sum += p[1]
			out = append(out, remaining(sum/float64(i+1)))
		}
		return out
	}
	series := hd.GetSeries()
	numS, denS := series.GetNumerator(), series.GetDenominator()
	num, den := numS.GetValues(), denS.GetValues()
	if len(num) > 1 && len(num) == len(den) {
		var cn, cd float64
		out := make([]float64, 0, len(num))
		for i := range num {
			cn += num[i]
			cd += den[i]
			if cd == 0 {
				continue
			}
			out = append(out, remaining(cn/cd*100))
		}
		return out
	}
	return nil
}

// SetIncidentField patches a single-value incident field — "state"
// (active/stable/resolved) or "severity" (SEV-1…SEV-5). Both share the same
// single-value attribute shape, so one method covers them. A write; UI-gated.
func (l *Live) SetIncidentField(ctx context.Context, id, field, value string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sv := datadogV2.NewIncidentFieldAttributesSingleValue()
	sv.SetValue(value)
	attrs := datadogV2.NewIncidentUpdateAttributes()
	attrs.SetFields(map[string]datadogV2.IncidentFieldAttributes{
		field: datadogV2.IncidentFieldAttributesSingleValueAsIncidentFieldAttributes(sv),
	})
	data := datadogV2.NewIncidentUpdateData(id, datadogV2.INCIDENTTYPE_INCIDENTS)
	data.SetAttributes(*attrs)
	body := datadogV2.NewIncidentUpdateRequest(*data)

	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncident(ctx, id, *body)
	l.track(resp)
	if err != nil {
		return apiErr("set incident "+field, err)
	}
	slog.Info("incident field changed", "id", id, "field", field, "value", value)
	return nil
}

// CurrentUser returns the acting user (GET /api/v2/current_user): its id drives
// commander assignment, its handle is the default to-do assignee.
func (l *Live) CurrentUser(ctx context.Context) (User, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, httpresp, err := datadogV2.NewUsersApi(l.client).GetCurrentUser(ctx)
	l.track(httpresp)
	if err != nil {
		return User{}, apiErr("current user", err)
	}
	u := resp.GetData()
	attrs := u.GetAttributes()
	handle := attrs.GetHandle()
	if handle == "" {
		handle = attrs.GetEmail()
	}
	return User{ID: u.GetId(), Handle: handle}, nil
}

// SetIncidentCommander assigns an incident's commander via the commander_user
// relationship on UpdateIncident (a write; UI-gated).
func (l *Live) SetIncidentCommander(ctx context.Context, incidentID, userID string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body := commanderUpdateBody(incidentID, userID)
	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncident(ctx, incidentID, body)
	l.track(resp)
	if err != nil {
		return apiErr("set incident commander", err)
	}
	slog.Info("incident commander assigned", "id", incidentID, "user", userID)
	return nil
}

// commanderUpdateBody builds the UpdateIncident request that sets commander_user
// to a user. Extracted so a test can assert its wire shape — the nested
// nullable-relationship construction is easy to get subtly wrong and can't be
// runtime-tested from the authoring sandbox.
func commanderUpdateBody(incidentID, userID string) datadogV2.IncidentUpdateRequest {
	relData := datadogV2.NewNullableRelationshipToUserData(userID, datadogV2.USERSTYPE_USERS)
	rel := datadogV2.NewNullableRelationshipToUser(*datadogV2.NewNullableNullableRelationshipToUserData(relData))
	rels := datadogV2.NewIncidentUpdateRelationships()
	rels.CommanderUser = *datadogV2.NewNullableNullableRelationshipToUser(rel)
	data := datadogV2.NewIncidentUpdateData(incidentID, datadogV2.INCIDENTTYPE_INCIDENTS)
	data.SetRelationships(*rels)
	return *datadogV2.NewIncidentUpdateRequest(*data)
}

// AddIncidentTodo adds a to-do (action item) to an incident, assigned to the
// given user handle (the API requires at least one assignee).
func (l *Live) AddIncidentTodo(ctx context.Context, incidentID, content, assigneeHandle string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	assignee := datadogV2.IncidentTodoAssigneeHandleAsIncidentTodoAssignee(&assigneeHandle)
	attrs := datadogV2.NewIncidentTodoAttributes([]datadogV2.IncidentTodoAssignee{assignee}, content)
	data := datadogV2.NewIncidentTodoCreateData(*attrs, datadogV2.INCIDENTTODOTYPE_INCIDENT_TODOS)
	body := datadogV2.NewIncidentTodoCreateRequest(*data)

	_, resp, err := datadogV2.NewIncidentsApi(l.client).CreateIncidentTodo(ctx, incidentID, *body)
	l.track(resp)
	if err != nil {
		return apiErr("add incident to-do", err)
	}
	slog.Info("incident to-do added", "id", incidentID, "assignee", assigneeHandle)
	return nil
}

// ListUsers searches active org users (GET /api/v2/users). The query is the
// server-side filter (name/email/handle); one bounded page so a picker never
// spends unbounded budget on an org with thousands of users.
func (l *Live) ListUsers(ctx context.Context, query string) ([]User, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	opts := datadogV2.NewListUsersOptionalParameters().
		WithFilterStatus("Active").
		WithPageSize(50)
	if query != "" {
		opts = opts.WithFilter(query)
	}
	resp, httpresp, err := datadogV2.NewUsersApi(l.client).ListUsers(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("list users", err)
	}
	data := resp.GetData()
	users := make([]User, 0, len(data))
	for _, u := range data {
		attrs := u.GetAttributes()
		handle := attrs.GetHandle()
		if handle == "" {
			handle = attrs.GetEmail()
		}
		users = append(users, User{ID: u.GetId(), Handle: handle, Name: attrs.GetName()})
	}
	return users, nil
}

// IncidentTodos lists an incident's to-dos (GET /api/v2/incidents/{id}/relationships/todos).
func (l *Live) IncidentTodos(ctx context.Context, incidentID string) ([]Todo, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewIncidentsApi(l.client).ListIncidentTodos(ctx, incidentID)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("list incident to-dos", err)
	}
	data := resp.GetData()
	todos := make([]Todo, 0, len(data))
	for _, td := range data {
		attrs := td.GetAttributes()
		todos = append(todos, Todo{
			ID:        td.GetId(),
			Content:   attrs.GetContent(),
			Assignees: todoAssigneeHandles(attrs.GetAssignees()),
			Completed: attrs.GetCompleted() != "", // non-empty completed timestamp = done
		})
	}
	return todos, nil
}

// SetIncidentTodoCompleted marks a to-do done (a completion timestamp) or
// undone (null). Content and assignees are re-sent from the loaded Todo so the
// PATCH doesn't blank them — the attributes constructor requires both.
func (l *Live) SetIncidentTodoCompleted(ctx context.Context, incidentID string, todo Todo, done bool) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body := todoCompletedPatchBody(todo, done, time.Now().UTC().Format(time.RFC3339))
	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncidentTodo(ctx, incidentID, todo.ID, body)
	l.track(resp)
	if err != nil {
		return apiErr("update incident to-do", err)
	}
	slog.Info("incident to-do completion changed", "id", incidentID, "todo", todo.ID, "done", done)
	return nil
}

// todoCompletedPatchBody builds the PATCH request that flips a to-do's
// completion. Extracted so a test can assert its wire shape: content and
// assignees are re-sent (the attributes constructor requires them) so the PATCH
// never blanks them; completed is the timestamp (done) or null (reopened).
func todoCompletedPatchBody(todo Todo, done bool, now string) datadogV2.IncidentTodoPatchRequest {
	assignees := make([]datadogV2.IncidentTodoAssignee, 0, len(todo.Assignees))
	for i := range todo.Assignees {
		h := todo.Assignees[i]
		assignees = append(assignees, datadogV2.IncidentTodoAssigneeHandleAsIncidentTodoAssignee(&h))
	}
	attrs := datadogV2.NewIncidentTodoAttributes(assignees, todo.Content)
	if done {
		attrs.SetCompleted(now)
	} else {
		attrs.SetCompletedNil()
	}
	data := datadogV2.NewIncidentTodoPatchData(*attrs, datadogV2.INCIDENTTODOTYPE_INCIDENT_TODOS)
	return *datadogV2.NewIncidentTodoPatchRequest(*data)
}

// DeleteIncidentTodo removes a to-do from an incident.
func (l *Live) DeleteIncidentTodo(ctx context.Context, incidentID, todoID string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := datadogV2.NewIncidentsApi(l.client).DeleteIncidentTodo(ctx, incidentID, todoID)
	l.track(resp)
	if err != nil {
		return apiErr("delete incident to-do", err)
	}
	slog.Info("incident to-do deleted", "id", incidentID, "todo", todoID)
	return nil
}

// todoAssigneeHandles pulls the handle from each to-do assignee (the handle
// arm of the assignee union); anonymous assignees are skipped.
func todoAssigneeHandles(as []datadogV2.IncidentTodoAssignee) []string {
	var out []string
	for i := range as {
		if h := as[i].IncidentTodoAssigneeHandle; h != nil && *h != "" {
			out = append(out, *h)
		}
	}
	return out
}

// incidentDetail builds the structured war-room summary from a fetched
// incident: title/severity/state/created/impact plus every non-empty field,
// alongside the resolved People and the raw object.
func incidentDetail(in datadogV2.IncidentResponse) *IncidentDetail {
	data := in.GetData()
	attrs := data.GetAttributes()
	fields := map[string]string{}
	for name := range attrs.GetFields() {
		if v := incidentField(attrs.GetFields(), name); v != "" {
			fields[name] = v
		}
	}
	return &IncidentDetail{
		Title:            attrs.GetTitle(),
		Severity:         string(attrs.GetSeverity()),
		State:            incidentField(attrs.GetFields(), "state"),
		Created:          attrs.GetCreated().Format(time.RFC3339),
		CustomerImpacted: attrs.GetCustomerImpacted(),
		ImpactScope:      attrs.GetCustomerImpactScope(),
		Fields:           fields,
		People:           incidentPeople(in),
		Incident:         in,
	}
}

// IncidentImpacts lists an incident's declared impacts, one line each.
func (l *Live) IncidentImpacts(ctx context.Context, incidentID string) ([]string, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewIncidentsApi(l.client).ListIncidentImpacts(ctx, incidentID)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("incident impacts", err)
	}
	var out []string
	for _, d := range resp.GetData() {
		attrs := d.GetAttributes()
		line := attrs.GetDescription()
		if t := attrs.GetImpactType(); t != "" {
			line = t + ": " + line
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// incidentPeople resolves an incident's commander/created/declared/responders
// to handles using the response's included users. Commander and created/declared
// ids are user ids (resolve cleanly); responder ids are a distinct object type
// with no include support, so an unresolved one falls back to its raw id.
func incidentPeople(in datadogV2.IncidentResponse) IncidentPeople {
	users := map[string]string{}
	for _, item := range in.GetIncluded() {
		if u := item.IncidentUserData; u != nil {
			users[u.GetId()] = incUserHandle(u)
		}
	}
	resolve := func(id string) string {
		if id == "" {
			return ""
		}
		if h, ok := users[id]; ok {
			return h
		}
		return id
	}

	data := in.GetData()
	rels := data.GetRelationships()
	var p IncidentPeople

	cu := rels.GetCommanderUser()
	cud := cu.GetData()
	p.Commander = resolve(cud.GetId())

	cb := rels.GetCreatedByUser()
	cbd := cb.GetData()
	p.CreatedBy = resolve(cbd.GetId())

	db := rels.GetDeclaredByUser()
	dbd := db.GetData()
	p.DeclaredBy = resolve(dbd.GetId())

	rr := rels.GetResponders()
	for _, rd := range rr.GetData() {
		if h := resolve(rd.GetId()); h != "" {
			p.Responders = append(p.Responders, h)
		}
	}
	return p
}

// incUserHandle prefers handle, then name, then email, then id.
func incUserHandle(u *datadogV2.IncidentUserData) string {
	attrs := u.GetAttributes()
	if h := attrs.GetHandle(); h != "" {
		return h
	}
	if n := attrs.GetName(); n != "" {
		return n
	}
	if e := attrs.GetEmail(); e != "" {
		return e
	}
	return u.GetId()
}

// SetMonitorMute mutes (indefinitely) or unmutes a monitor. It is a
// read-modify-write on the monitor's options so muting never clobbers
// thresholds, renotify, or any other option: fetch the monitor, flip only
// options.silenced ({"*":0} = mute all scopes; {} = unmute), write back.
func (l *Live) SetMonitorMute(ctx context.Context, id string, mute bool) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	mid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("monitor id %q: %w", id, err)
	}
	api := datadogV1.NewMonitorsApi(l.client)
	mon, resp, err := api.GetMonitor(ctx, mid, *datadogV1.NewGetMonitorOptionalParameters())
	l.track(resp)
	if err != nil {
		return apiErr("mute: read monitor", err)
	}
	opts := mon.GetOptions()
	if mute {
		opts.Silenced = map[string]int64{"*": 0} // 0 = mute with no end time
	} else {
		opts.Silenced = map[string]int64{}
	}
	body := datadogV1.NewMonitorUpdateRequest()
	body.SetOptions(opts)
	_, resp2, err := api.UpdateMonitor(ctx, mid, *body)
	l.track(resp2)
	if err != nil {
		return apiErr("mute: update monitor", err)
	}
	slog.Info("monitor mute changed", "id", id, "muted", mute)
	return nil
}

// CancelDowntime cancels a scheduled/active downtime by id (v2 Downtimes API).
// A write; UI-gated behind confirmation.
func (l *Live) CancelDowntime(ctx context.Context, id string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := datadogV2.NewDowntimesApi(l.client).CancelDowntime(ctx, id)
	l.track(resp)
	if err != nil {
		return apiErr("cancel downtime", err)
	}
	slog.Info("downtime cancelled", "id", id)
	return nil
}

// MaxDashWidgets bounds how many metric sparklines one dashboard render will
// fetch. The timeseries query API is the tightest budget we spend, so a
// 40-widget dashboard cannot fan out to 40 requests on a single open.
const MaxDashWidgets = 12

// Dashboard renders a dashboard: fetch its definition, flatten the widget
// tree, and fetch a sparkline for each metric widget (bounded). Widgets we
// can't chart (log streams, notes, formula-only queries) still appear, with
// a note instead of a sparkline.
func (l *Live) Dashboard(ctx context.Context, id string) (*DashboardView, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	d, resp, err := datadogV1.NewDashboardsApi(l.client).GetDashboard(ctx, id)
	l.track(resp)
	if err != nil {
		return nil, apiErr("dashboard render", err)
	}
	// Walk the definition generically: the widget-definition union has ~25
	// variants and nests (group widgets contain widgets); JSON traversal is
	// far more robust than the typed union for pulling title/type/query.
	raw, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)

	view := &DashboardView{Title: d.GetTitle()}
	var widgets []Widget
	collectWidgets(m["widgets"], &widgets)

	metricAPI := datadogV1.NewMetricsApi(l.client)
	from := time.Now().Add(-time.Hour).Unix()
	to := time.Now().Unix()
	fetched := 0
	for i := range widgets {
		w := &widgets[i]
		if w.Query == "" {
			w.Note = "no single metric query (formula/log/note widget)"
			continue
		}
		if fetched >= MaxDashWidgets {
			w.Note = "sparkline budget reached — open in Datadog (o)"
			view.Truncated = true
			continue
		}
		fetched++
		mq, mresp, err := metricAPI.QueryMetrics(ctx, from, to, w.Query)
		l.track(mresp)
		if err != nil {
			w.Note = "query failed"
			slog.Debug("widget query failed", "title", w.Title, "err", err)
			continue
		}
		if pts := firstSeriesPoints(mq); len(pts) > 0 {
			w.Spark = pts
			w.Last = pts[len(pts)-1]
			w.HasData = true
		} else {
			w.Note = "no data in last 1h"
		}
	}
	view.Widgets = widgets
	if view.Truncated {
		slog.Warn("dashboard sparklines truncated", "dashboard", id, "cap", MaxDashWidgets)
	}
	return view, nil
}

// collectWidgets flattens the (possibly nested) widget tree in definition
// order, pulling title, type and a single metric query from each.
func collectWidgets(node any, out *[]Widget) {
	list, ok := node.([]any)
	if !ok {
		return
	}
	for _, item := range list {
		wobj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		def, _ := wobj["definition"].(map[string]any)
		if def == nil {
			continue
		}
		// Group widget: recurse into its children, don't emit a row for it.
		if nested, ok := def["widgets"]; ok {
			collectWidgets(nested, out)
			continue
		}
		title, _ := def["title"].(string)
		typ, _ := def["type"].(string)
		if title == "" {
			title = "(untitled)"
		}
		w := Widget{Title: title, Type: typ, Query: widgetQuery(def)}
		// Layout (free/grid dashboards): x/y/width/height in grid units;
		// absent for ordered layouts (W stays 0 → renderer falls back to flow).
		if lay, ok := wobj["layout"].(map[string]any); ok {
			w.X = jsonInt(lay["x"])
			w.Y = jsonInt(lay["y"])
			w.W = jsonInt(lay["width"])
			w.H = jsonInt(lay["height"])
		}
		*out = append(*out, w)
	}
}

// jsonInt coerces a JSON number (float64) or numeric string to int.
func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// widgetQuery extracts a single runnable metric query from a widget
// definition, best-effort. Classic widgets carry requests[].q; formula
// widgets carry requests[].queries[] — we take the first metrics query.
// Multi-query formula widgets return "" (not runnable as one query).
func widgetQuery(def map[string]any) string {
	reqs := def["requests"]
	// query_value widgets sometimes have requests as an object, not a list.
	var reqList []any
	switch r := reqs.(type) {
	case []any:
		reqList = r
	case map[string]any:
		reqList = []any{r}
	default:
		return ""
	}
	for _, ri := range reqList {
		req, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		if q, ok := req["q"].(string); ok && q != "" {
			return q
		}
		if qs, ok := req["queries"].([]any); ok && len(qs) == 1 {
			if q0, ok := qs[0].(map[string]any); ok {
				if ds, _ := q0["data_source"].(string); ds == "metrics" {
					if q, ok := q0["query"].(string); ok && q != "" {
						return q
					}
				}
			}
		}
	}
	return ""
}

// firstSeriesPoints extracts the value series from the first returned metric
// series, dropping null points.
func firstSeriesPoints(mq datadogV1.MetricsQueryResponse) []float64 {
	series := mq.GetSeries()
	if len(series) == 0 {
		return nil
	}
	var pts []float64
	for _, pair := range series[0].GetPointlist() {
		if len(pair) == 2 && pair[1] != nil {
			pts = append(pts, *pair[1])
		}
	}
	return pts
}

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

func (l *Live) monitors(ctx context.Context) ([]Row, error) {
	api := datadogV1.NewMonitorsApi(l.client)
	var rows []Row
	for page := int64(0); page < maxMonitorPages; page++ {
		mons, resp, err := api.ListMonitors(ctx,
			*datadogV1.NewListMonitorsOptionalParameters().WithPageSize(monitorPageSize).WithPage(page))
		l.track(resp)
		if err != nil {
			return nil, apiErr("monitors", err)
		}
		for _, m := range mons {
			prio := ""
			if p, ok := m.GetPriorityOk(); ok && p != nil {
				prio = fmt.Sprintf("P%d", *p)
			}
			muted := monitorMuted(m.GetOptions())
			rows = append(rows, Row{
				ID:       fmt.Sprintf("%d", m.GetId()),
				Cells:    []string{string(m.GetOverallState()), mutedCell(muted), m.GetName(), string(m.GetType()), prio, strings.Join(m.GetTags(), ",")},
				Raw:      m,
				URL:      fmt.Sprintf("%s/monitors/%d", l.web, m.GetId()),
				LogQuery: monitorLogQuery(m),
				Muted:    muted,
			})
		}
		if len(mons) < monitorPageSize {
			SortMonitors(rows)
			return rows, nil
		}
	}
	slog.Warn("monitor list truncated", "cap", maxMonitorPages*monitorPageSize)
	SortMonitors(rows)
	return rows, nil
}

// monitorMuted reports whether a monitor is currently silenced. Datadog's
// options.silenced maps a scope ("*" or a tag scope) to an end timestamp:
// 0 means muted with no end, a future unix time means muted until then, a
// past time is an expired (ineffective) entry. Muted iff any entry is 0 or
// in the future.
func monitorMuted(opts datadogV1.MonitorOptions) bool {
	now := time.Now().Unix()
	for _, end := range opts.GetSilenced() {
		if end == 0 || end > now {
			return true
		}
	}
	return false
}

func mutedCell(muted bool) string {
	if muted {
		return "muted"
	}
	return ""
}

// monitorLogQuery derives a Datadog logs search query for the monitor →
// logs drill-down ('l'). Log monitors carry their exact query inside
// logs("…"); for everything else, service:/env: tags are a good heuristic.
func monitorLogQuery(m datadogV1.Monitor) string {
	if string(m.GetType()) == "log alert" {
		q := m.GetQuery()
		if i := strings.Index(q, `logs("`); i >= 0 {
			rest := q[i+len(`logs("`):]
			if j := strings.Index(rest, `")`); j >= 0 && rest[:j] != "" {
				return rest[:j]
			}
		}
	}
	var parts []string
	for _, tag := range m.GetTags() {
		if strings.HasPrefix(tag, "service:") || strings.HasPrefix(tag, "env:") {
			parts = append(parts, tag)
		}
	}
	return strings.Join(parts, " ")
}

func (l *Live) incidents(ctx context.Context) ([]Row, error) {
	api := datadogV2.NewIncidentsApi(l.client)
	var data []datadogV2.IncidentResponseData
	for page := int64(0); page < maxIncidentPage; page++ {
		resp, httpresp, err := api.ListIncidents(ctx,
			*datadogV2.NewListIncidentsOptionalParameters().
				WithPageSize(incidentPage).WithPageOffset(page * incidentPage))
		l.track(httpresp)
		if err != nil {
			return nil, apiErr("incidents", err)
		}
		got := resp.GetData()
		data = append(data, got...)
		if int64(len(got)) < incidentPage {
			break
		}
		if page == maxIncidentPage-1 {
			slog.Warn("incident list truncated", "cap", maxIncidentPage*incidentPage)
		}
	}
	rows := make([]Row, 0, len(data))
	for _, d := range data {
		a := d.GetAttributes()
		sev := incidentField(a.GetFields(), "severity")
		state := incidentField(a.GetFields(), "state")
		impact := ""
		if a.GetCustomerImpacted() {
			impact = "customer"
		}
		publicID := fmt.Sprintf("%d", a.GetPublicId())
		rows = append(rows, Row{
			ID:    d.GetId(),
			Cells: []string{"IR-" + publicID, sev, state, a.GetTitle(), impact, a.GetCreated().Local().Format("2006-01-02 15:04")},
			Raw:   d,
			URL:   l.web + "/incidents/" + publicID,
		})
	}
	return rows, nil
}

// incidentField reads an incident field value, handling both arms of the
// IncidentFieldAttributes union: single-value fields (state, severity) return
// their value; multi-value fields (multiselect custom fields) join their
// values. A missing field or an unparsed variant yields "" rather than
// breaking the row — real orgs carry custom fields of either shape.
func incidentField(fields map[string]datadogV2.IncidentFieldAttributes, key string) string {
	f, ok := fields[key]
	if !ok {
		return ""
	}
	if sv := f.IncidentFieldAttributesSingleValue; sv != nil {
		return sv.GetValue()
	}
	if mv := f.IncidentFieldAttributesMultipleValue; mv != nil {
		return strings.Join(mv.GetValue(), ", ")
	}
	return ""
}

func (l *Live) slos(ctx context.Context) ([]Row, error) {
	api := datadogV1.NewServiceLevelObjectivesApi(l.client)
	resp, httpresp, err := api.ListSLOs(ctx,
		*datadogV1.NewListSLOsOptionalParameters().WithLimit(1000))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("slos", err)
	}
	data := resp.GetData()
	// Follow offsets if the org has more SLOs than one page.
	for page := int64(1); page < maxSLOPages && int64(len(data)) == page*sloPageSize; page++ {
		more, httpresp2, err := api.ListSLOs(ctx,
			*datadogV1.NewListSLOsOptionalParameters().WithLimit(sloPageSize).WithOffset(page * sloPageSize))
		l.track(httpresp2)
		if err != nil {
			return nil, apiErr("slos", err)
		}
		data = append(data, more.GetData()...)
		if page == maxSLOPages-1 && int64(len(more.GetData())) == sloPageSize {
			slog.Warn("slo list truncated", "cap", maxSLOPages*sloPageSize)
		}
	}
	rows := make([]Row, 0, len(data))
	for _, s := range data {
		target, timeframe := "", ""
		if th := s.GetThresholds(); len(th) > 0 {
			target = fmt.Sprintf("%.2f%%", th[0].GetTarget())
			timeframe = string(th[0].GetTimeframe())
		}
		rows = append(rows, Row{
			ID:    s.GetId(),
			Cells: []string{s.GetName(), string(s.GetType()), target, timeframe, strings.Join(s.GetTags(), ",")},
			Raw:   s,
			URL:   l.web + "/slo?slo_id=" + s.GetId(),
		})
	}
	return rows, nil
}

func (l *Live) logs(ctx context.Context, query, timeRange string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	if timeRange == "" {
		timeRange = "now-15m"
	}
	api := datadogV2.NewLogsApi(l.client)
	body := datadogV2.LogsListRequest{
		Filter: &datadogV2.LogsQueryFilter{
			Query: datadog.PtrString(query),
			From:  datadog.PtrString(timeRange),
			To:    datadog.PtrString("now"),
		},
		Sort: datadogV2.LOGSSORT_TIMESTAMP_DESCENDING.Ptr(),
		Page: &datadogV2.LogsListRequestPage{Limit: datadog.PtrInt32(100)},
	}
	resp, httpresp, err := api.ListLogs(ctx,
		*datadogV2.NewListLogsOptionalParameters().WithBody(body))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("logs", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, lg := range data {
		a := lg.GetAttributes()
		msg := a.GetMessage()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		rows = append(rows, Row{
			ID:      lg.GetId(),
			Cells:   []string{a.GetTimestamp().Local().Format("15:04:05"), a.GetStatus(), a.GetService(), a.GetHost(), msg},
			Raw:     lg,
			URL:     l.web + "/logs?query=" + url.QueryEscape(query),
			TraceID: traceIDFromAttrs(a.GetAttributes()),
		})
	}
	return rows, nil
}

// traceIDFromAttrs digs the trace id out of a log's nested attribute map.
// Datadog APM log-injection puts it at "trace_id", "dd.trace_id", or nested
// under "dd":{"trace_id"} depending on the tracer/config. Returns "" if the
// log isn't correlated to a trace.
func traceIDFromAttrs(attrs map[string]interface{}) string {
	if attrs == nil {
		return ""
	}
	for _, k := range []string{"trace_id", "dd.trace_id"} {
		if v, ok := attrs[k]; ok {
			if s := stringifyID(v); s != "" {
				return s
			}
		}
	}
	if dd, ok := attrs["dd"].(map[string]interface{}); ok {
		if s := stringifyID(dd["trace_id"]); s != "" {
			return s
		}
	}
	return ""
}

// stringifyID renders a trace/span id that may arrive as a string or a
// JSON number (float64) without scientific-notation mangling.
func stringifyID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

func (l *Live) dashboards(ctx context.Context) ([]Row, error) {
	api := datadogV1.NewDashboardsApi(l.client)
	var dashs []datadogV1.DashboardSummaryDefinition
	for page := int64(0); page < maxDashPages; page++ {
		resp, httpresp, err := api.ListDashboards(ctx,
			*datadogV1.NewListDashboardsOptionalParameters().
				WithCount(dashPageSize).WithStart(page * dashPageSize))
		l.track(httpresp)
		if err != nil {
			return nil, apiErr("dashboards", err)
		}
		got := resp.GetDashboards()
		dashs = append(dashs, got...)
		if int64(len(got)) < dashPageSize {
			break
		}
		if page == maxDashPages-1 {
			slog.Warn("dashboard list truncated", "cap", maxDashPages*dashPageSize)
		}
	}
	rows := make([]Row, 0, len(dashs))
	for _, d := range dashs {
		rows = append(rows, Row{
			ID:    d.GetId(),
			Cells: []string{d.GetTitle(), string(d.GetLayoutType()), d.GetAuthorHandle(), d.GetModifiedAt().Local().Format("2006-01-02 15:04")},
			Raw:   d,
			URL:   l.web + d.GetUrl(),
		})
	}
	return rows, nil
}

const eventsPageLimit = 100 // one page of events per fetch

// events lists the Datadog event stream — the "what changed" feed (deploys,
// alerts, config changes, comments). Server-side query like logs/traces.
func (l *Live) events(ctx context.Context, query, timeRange string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	if timeRange == "" {
		timeRange = "now-4h" // events are sparser than logs; a wider default helps
	}
	api := datadogV2.NewEventsApi(l.client)
	resp, httpresp, err := api.ListEvents(ctx,
		*datadogV2.NewListEventsOptionalParameters().
			WithFilterQuery(query).WithFilterFrom(timeRange).WithFilterTo("now").
			WithSort(datadogV2.EVENTSSORT_TIMESTAMP_DESCENDING).
			WithPageLimit(eventsPageLimit))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("events", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, e := range data {
		ra := e.GetAttributes()  // EventResponseAttributes: message, tags, timestamp
		ea := ra.GetAttributes() // EventAttributes: title, status, source, service
		title := ea.GetTitle()
		if title == "" {
			title = firstLine(ra.GetMessage())
		}
		source := ea.GetSourceTypeName()
		if source == "" {
			source = ea.GetService()
		}
		rows = append(rows, Row{
			ID:    e.GetId(),
			Cells: []string{ra.GetTimestamp().Local().Format("2006-01-02 15:04"), string(ea.GetStatus()), source, title, strings.Join(ra.GetTags(), ",")},
			Raw:   e,
			URL:   l.web + "/event/explorer",
		})
	}
	return rows, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// downtimes lists scheduled/active mutes org-wide — the visibility partner
// to the per-monitor MUTED column and the m mute action.
func (l *Live) downtimes(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV2.NewDowntimesApi(l.client).ListDowntimes(ctx,
		*datadogV2.NewListDowntimesOptionalParameters().WithPageLimit(100))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("downtimes", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, d := range data {
		a := d.GetAttributes()
		rows = append(rows, Row{
			ID:    d.GetId(),
			Cells: []string{string(a.GetStatus()), a.GetScope(), firstLine(a.GetMessage()), a.GetCreated().Local().Format("2006-01-02 15:04")},
			Raw:   d,
			URL:   l.web + "/monitors/downtimes",
		})
	}
	return rows, nil
}

const spansPageLimit = 100 // one page of spans per search (bounded budget)

// spans searches APM span events (the Traces view). Server-side query like
// logs; each row links to its trace for the waterfall drill-down.
func (l *Live) spans(ctx context.Context, query, timeRange string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	if timeRange == "" {
		timeRange = "now-15m"
	}
	api := datadogV2.NewSpansApi(l.client)
	resp, httpresp, err := api.ListSpansGet(ctx,
		*datadogV2.NewListSpansGetOptionalParameters().
			WithFilterQuery(query).WithFilterFrom(timeRange).WithFilterTo("now").
			WithSort(datadogV2.SPANSSORT_TIMESTAMP_DESCENDING).
			WithPageLimit(spansPageLimit))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("spans", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, s := range data {
		a := s.GetAttributes()
		errMark := ""
		if spanIsError(a) {
			errMark = "error"
		}
		rows = append(rows, Row{
			ID:       s.GetId(),
			TraceID:  a.GetTraceId(),
			LogQuery: "trace_id:" + a.GetTraceId(), // l → logs for this trace
			Cells: []string{
				a.GetStartTimestamp().Local().Format("15:04:05"),
				a.GetService(), a.GetResourceName(),
				FormatDuration(spanDurationUs(a)), errMark, a.GetTraceId(),
			},
			Raw: s,
			URL: l.web + "/apm/trace/" + a.GetTraceId(),
		})
	}
	return rows, nil
}

// servicesDefaultEnv is the APM env used when the :services query is unset. The
// service-list endpoint is env-scoped (filter[env]), so an env is always sent.
const servicesDefaultEnv = "prod"

// services lists the org's APM services for an environment (the '/' query sets
// the env, default "prod"). It uses GET /api/v2/apm/services, which is derived
// from trace stats and therefore independent of span indexing/retention — a
// span aggregate returns nothing when retention filters drop spans, which is
// why the earlier implementation showed empty on orgs with tight retention.
// Names only: the official API does not expose per-service request/error/
// latency stats to third-party clients (that lives in an internal endpoint).
// enter → that service's traces.
func (l *Live) services(ctx context.Context, query, _ string) ([]Row, error) {
	env := strings.TrimSpace(query)
	if env == "" || env == "*" {
		env = servicesDefaultEnv
	}
	resp, httpresp, err := datadogV2.NewAPMApi(l.client).GetServiceList(ctx, env)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("services", err)
	}
	data := resp.GetData()
	attrs := data.GetAttributes()
	names := attrs.GetServices()
	sort.Strings(names)
	rows := make([]Row, 0, len(names))
	for _, n := range names {
		rows = append(rows, Row{
			ID:    n,
			Cells: []string{n},
			URL:   l.web + "/apm/services/" + n,
		})
	}
	return rows, nil
}

// Trace fetches a distributed trace by id via the APM get-trace endpoint
// (GET /api/v2/trace/{id}, an unstable operation enabled at client init) and
// links its spans by parent id into a DFS-ordered tree for the waterfall. One
// call, with the API's own truncation flag — this is the canonical trace fetch
// and replaces the older reconstruction from a trace_id: span search.
func (l *Live) Trace(ctx context.Context, traceID string) (*TraceView, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewAPMTraceApi(l.client).GetTraceByID(ctx, traceID)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("trace", err)
	}
	attrs := resp.Data.Attributes
	nodes := make([]Span, 0, len(attrs.Spans))
	for _, s := range attrs.Spans {
		parent := "" // ParentId 0 == trace root
		if s.ParentId != 0 {
			parent = strconv.FormatUint(uint64(s.ParentId), 10)
		}
		nodes = append(nodes, Span{
			ID:         strconv.FormatUint(uint64(s.SpanId), 10),
			ParentID:   parent,
			Service:    s.Service,
			Resource:   s.Resource,
			OffsetUs:   s.StartTime / 1000, // Unix ns → µs
			DurationUs: s.Duration / 1000,  // ns → µs
			Error:      s.Error == datadogV2.APMSPANERRORFLAG_ERROR,
		})
	}
	view := buildTrace(traceID, nodes)
	view.Truncated = attrs.IsTruncated
	view.Logs = l.traceLogs(ctx, traceID) // best-effort; empty if uncorrelated
	return view, nil
}

// traceLogs fetches this trace's logs across all services, oldest-first, so
// the trace view can show a unified request timeline. Best-effort: any error
// (or logs without trace_id) just yields no timeline, never fails the trace.
func (l *Live) traceLogs(ctx context.Context, traceID string) []TraceLog {
	body := datadogV2.LogsListRequest{
		Filter: &datadogV2.LogsQueryFilter{
			Query: datadog.PtrString("trace_id:" + traceID),
			From:  datadog.PtrString("now-4h"),
			To:    datadog.PtrString("now"),
		},
		Sort: datadogV2.LOGSSORT_TIMESTAMP_ASCENDING.Ptr(),
		Page: &datadogV2.LogsListRequestPage{Limit: datadog.PtrInt32(100)},
	}
	resp, httpresp, err := datadogV2.NewLogsApi(l.client).ListLogs(ctx,
		*datadogV2.NewListLogsOptionalParameters().WithBody(body))
	l.track(httpresp)
	if err != nil {
		slog.Debug("trace logs fetch failed", "trace", traceID, "err", err)
		return nil
	}
	data := resp.GetData()
	out := make([]TraceLog, 0, len(data))
	for _, lg := range data {
		a := lg.GetAttributes()
		out = append(out, TraceLog{
			Time:    a.GetTimestamp(),
			Service: a.GetService(),
			Status:  a.GetStatus(),
			Message: firstLine(a.GetMessage()),
		})
	}
	return out
}

// MonitorMetric fetches a monitor's evaluated metric over the last hour.
// The runnable metric query is extracted best-effort from the monitor's
// alert query; non-metric monitors (service checks, log/query alerts that
// don't map to a single timeseries) return a Note instead of points.
func (l *Live) MonitorMetric(ctx context.Context, id string) (*MetricSeries, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	mid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("monitor id %q: %w", id, err)
	}
	m, resp, err := datadogV1.NewMonitorsApi(l.client).GetMonitor(ctx, mid,
		*datadogV1.NewGetMonitorOptionalParameters())
	l.track(resp)
	if err != nil {
		return nil, apiErr("monitor metric", err)
	}
	mq := extractMonitorMetricQuery(m.GetQuery())
	if mq == "" {
		return &MetricSeries{Note: "no single metric query to chart (service check / log / composite monitor)"}, nil
	}
	from := time.Now().Add(-time.Hour).Unix()
	to := time.Now().Unix()
	res, mresp, err := datadogV1.NewMetricsApi(l.client).QueryMetrics(ctx, from, to, mq)
	l.track(mresp)
	if err != nil {
		return &MetricSeries{Query: mq, Note: "query failed: " + err.Error()}, nil
	}
	pts := firstSeriesPoints(res)
	ms := &MetricSeries{Query: mq, Points: pts}
	if len(pts) == 0 {
		ms.Note = "no data in the last 1h"
	} else {
		ms.Last = pts[len(pts)-1]
	}
	return ms, nil
}

// extractMonitorMetricQuery pulls the runnable metric query out of a monitor
// alert query like "avg(last_5m):avg:system.cpu.user{*} > 90" → the middle
// "avg:system.cpu.user{*}". Returns "" when there's no "):"-delimited metric
// body (service checks, log alerts, event alerts, etc.).
func extractMonitorMetricQuery(q string) string {
	i := strings.Index(q, "):")
	if i < 0 {
		return ""
	}
	body := q[i+2:]
	// Trim a trailing comparison "... > 90" / ">= 0.9" etc.
	for _, op := range []string{" >= ", " <= ", " > ", " < ", " == ", " != "} {
		if j := strings.LastIndex(body, op); j >= 0 {
			body = body[:j]
			break
		}
	}
	body = strings.TrimSpace(body)
	// A metric query needs a scope; bail on anything that doesn't look like one.
	if body == "" || !strings.ContainsAny(body, "{:") {
		return ""
	}
	return body
}

// spanDurationUs returns a span's duration in microseconds (end - start).
func spanDurationUs(a datadogV2.SpansAttributes) int64 {
	d := a.GetEndTimestamp().Sub(a.GetStartTimestamp()).Microseconds()
	if d < 0 {
		return 0
	}
	return d
}

// spanIsError checks the span's custom/attribute maps for an error marker
// (error flag or an HTTP status >= 500).
func spanIsError(a datadogV2.SpansAttributes) bool {
	for _, m := range []map[string]interface{}{a.GetCustom(), a.GetAttributes()} {
		if truthy(m["error"]) {
			return true
		}
		if code := toInt(m["http.status_code"]); code >= 500 {
			return true
		}
	}
	return false
}

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
