package data

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

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
			Cells:   []string{a.GetTimestamp().Local().Format("15:04:05"), a.GetStatus(), a.GetService(), a.GetHost(), msg, strings.Join(a.GetTags(), " ")},
			Raw:     lg,
			URL:     l.web + "/logs?query=" + url.QueryEscape(query),
			TraceID: traceIDFromAttrs(a.GetAttributes()),
		})
	}
	return rows, nil
}

// LogContext fetches the log events in a ±window around the anchor line,
// scoped to the same service (and host, when known), oldest first. It is a
// single bounded search — never polls — so it is cheap and rate-limit-safe.
func (l *Live) LogContext(ctx context.Context, anchor Row, windowSecs int) (*LogContextView, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	lg, ok := anchor.Raw.(datadogV2.Log)
	if !ok {
		return nil, fmt.Errorf("log context: this row is not a log")
	}
	attrs := lg.GetAttributes()
	anchorTS := attrs.GetTimestamp()
	svc, host := attrs.GetService(), attrs.GetHost()

	if windowSecs <= 0 {
		windowSecs = 300 // ±5 minutes
	}
	win := time.Duration(windowSecs) * time.Second
	from, to := anchorTS.Add(-win), anchorTS.Add(win)

	var parts []string
	if svc != "" {
		parts = append(parts, "service:"+quoteFacet(svc))
	}
	if host != "" {
		parts = append(parts, "host:"+quoteFacet(host))
	}
	query := "*"
	if len(parts) > 0 {
		query = strings.Join(parts, " ")
	}

	body := datadogV2.LogsListRequest{
		Filter: &datadogV2.LogsQueryFilter{
			Query: datadog.PtrString(query),
			From:  datadog.PtrString(strconv.FormatInt(from.UnixMilli(), 10)),
			To:    datadog.PtrString(strconv.FormatInt(to.UnixMilli(), 10)),
		},
		Sort: datadogV2.LOGSSORT_TIMESTAMP_ASCENDING.Ptr(),
		Page: &datadogV2.LogsListRequestPage{Limit: datadog.PtrInt32(200)},
	}
	resp, httpresp, err := datadogV2.NewLogsApi(l.client).ListLogs(ctx,
		*datadogV2.NewListLogsOptionalParameters().WithBody(body))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("log context", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, e := range data {
		a := e.GetAttributes()
		msg := a.GetMessage()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		rows = append(rows, Row{
			ID:      e.GetId(),
			Cells:   []string{a.GetTimestamp().Local().Format("15:04:05.000"), a.GetStatus(), a.GetService(), a.GetHost(), msg},
			Raw:     e,
			TraceID: traceIDFromAttrs(a.GetAttributes()),
		})
	}
	return &LogContextView{AnchorID: anchor.ID, Service: svc, Host: host, Window: win, Rows: rows}, nil
}

// quoteFacet quotes a Datadog facet value so spaces or special characters in a
// service/host name can't break out of the search term.
func quoteFacet(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
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

// LogCounts aggregates a logs query into counts by facet (one bounded call).
func (l *Live) LogCounts(ctx context.Context, query, timeRange, facet string) ([]WidgetItem, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	if timeRange == "" {
		timeRange = "now-15m"
	}
	if facet == "" {
		facet = "service"
	}
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := datadogV2.NewLogsApi(l.client)
	count := datadogV2.LOGSAGGREGATIONFUNCTION_COUNT
	body := datadogV2.LogsAggregateRequest{
		Compute: []datadogV2.LogsCompute{{Aggregation: count}},
		Filter: &datadogV2.LogsQueryFilter{
			Query: datadog.PtrString(query),
			From:  datadog.PtrString(timeRange),
			To:    datadog.PtrString("now"),
		},
		GroupBy: []datadogV2.LogsGroupBy{{
			Facet: facet,
			Limit: datadog.PtrInt64(10),
			Sort: &datadogV2.LogsAggregateSort{
				Aggregation: &count,
				Order:       datadogV2.LOGSSORTORDER_DESCENDING.Ptr(),
			},
		}},
	}
	resp, httpresp, err := api.AggregateLogs(ctx, body)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("logs aggregate", err)
	}
	respData := resp.GetData()
	buckets := respData.GetBuckets()
	items := make([]WidgetItem, 0, len(buckets))
	for _, b := range buckets {
		label := ""
		for _, v := range b.GetBy() {
			if s, ok := v.(string); ok {
				label = s
				break
			}
		}
		if label == "" {
			label = "(none)"
		}
		for _, cv := range b.GetComputes() {
			if cv.LogsAggregateBucketValueSingleNumber != nil {
				items = append(items, WidgetItem{Label: label, Value: *cv.LogsAggregateBucketValueSingleNumber})
				break
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Value > items[j].Value })
	return items, nil
}
