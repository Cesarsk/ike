package data

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

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
				strings.Join(a.GetTags(), " "),
			},
			Raw: s,
			URL: l.web + "/apm/trace/" + a.GetTraceId(),
		})
	}
	return rows, nil
}

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
	catalog := l.serviceCatalog(ctx)
	rows := make([]Row, 0, len(names))
	for _, n := range names {
		meta := catalog[n]
		rows = append(rows, Row{
			ID:    n,
			Cells: []string{n, meta[0], meta[1], meta[2], meta[3]},
			URL:   l.web + "/apm/services/" + n,
		})
	}
	return rows, nil
}

// serviceCatalog fetches the service-catalog definitions and maps service
// name → [team, tier, lifecycle, tags]. Best-effort (an org without the
// catalog gets blank columns, never an error); the schema union has many
// versions, so a generic JSON walk beats the typed union.
func (l *Live) serviceCatalog(ctx context.Context) map[string][4]string {
	const maxDefs = 500
	out := map[string][4]string{}
	ch, cancel := datadogV2.NewServiceDefinitionApi(l.client).ListServiceDefinitionsWithPagination(ctx,
		*datadogV2.NewListServiceDefinitionsOptionalParameters().WithPageSize(100))
	defer cancel()
	for item := range ch {
		if item.Error != nil {
			slog.Debug("service catalog unavailable", "err", item.Error)
			break
		}
		itemAttrs := item.Item.GetAttributes()
		raw, err := json.Marshal(itemAttrs.GetSchema())
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		name, _ := m["dd-service"].(string)
		if name == "" {
			continue
		}
		team, _ := m["team"].(string)
		tier, _ := m["tier"].(string)
		life, _ := m["lifecycle"].(string)
		var tags []string
		if raw, ok := m["tags"].([]any); ok {
			for _, t := range raw {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		out[name] = [4]string{team, tier, life, strings.Join(tags, " ")}
		if len(out) >= maxDefs {
			break
		}
	}
	return out
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
