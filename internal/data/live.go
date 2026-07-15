package data

import (
	"context"
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
	mu     sync.Mutex
	limits map[string]string
}

func newLive(site, webBase string) *Live {
	cfg := datadog.NewConfiguration()
	cfg.SetUnstableOperationEnabled("v2.ListIncidents", true)
	cfg.SetUnstableOperationEnabled("v2.GetIncident", true)
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

// authCtx attaches this org's credentials and site to a request context.
func (l *Live) authCtx(parent context.Context) context.Context {
	var ctx context.Context
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

func (l *Live) Fetch(ctx context.Context, key, query string) ([]Row, error) {
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
		return l.logs(ctx, query)
	case "dashboards":
		return l.dashboards(ctx)
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
		return m, nil
	case "dashboards":
		d, resp, err := datadogV1.NewDashboardsApi(l.client).GetDashboard(ctx, id)
		l.track(resp)
		if err != nil {
			return nil, apiErr("dashboard detail", err)
		}
		return d, nil
	case "incidents":
		in, resp, err := datadogV2.NewIncidentsApi(l.client).GetIncident(ctx, id,
			*datadogV2.NewGetIncidentOptionalParameters())
		l.track(resp)
		if err != nil {
			return nil, apiErr("incident detail", err)
		}
		return in, nil
	}
	return nil, nil // the list row is already the full object
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
			rows = append(rows, Row{
				ID:       fmt.Sprintf("%d", m.GetId()),
				Cells:    []string{string(m.GetOverallState()), m.GetName(), string(m.GetType()), prio, strings.Join(m.GetTags(), ",")},
				Raw:      m,
				URL:      fmt.Sprintf("%s/monitors/%d", l.web, m.GetId()),
				LogQuery: monitorLogQuery(m),
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

func incidentField(fields map[string]datadogV2.IncidentFieldAttributes, key string) string {
	f, ok := fields[key]
	if !ok {
		return ""
	}
	if sv := f.IncidentFieldAttributesSingleValue; sv != nil {
		return sv.GetValue()
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

func (l *Live) logs(ctx context.Context, query string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	api := datadogV2.NewLogsApi(l.client)
	body := datadogV2.LogsListRequest{
		Filter: &datadogV2.LogsQueryFilter{
			Query: datadog.PtrString(query),
			From:  datadog.PtrString("now-15m"),
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
			ID:    lg.GetId(),
			Cells: []string{a.GetTimestamp().Local().Format("15:04:05"), a.GetStatus(), a.GetService(), a.GetHost(), msg},
			Raw:   lg,
			URL:   l.web + "/logs?query=" + url.QueryEscape(query),
		})
	}
	return rows, nil
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

func apiErr(what string, err error) error {
	if oe, ok := err.(datadog.GenericOpenAPIError); ok && len(oe.Body()) > 0 {
		body := string(oe.Body())
		if len(body) > 200 {
			body = body[:200]
		}
		return fmt.Errorf("%s: %s — %s", what, err.Error(), body)
	}
	return fmt.Errorf("%s: %w", what, err)
}
