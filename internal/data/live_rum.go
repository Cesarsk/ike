package data

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

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
				strings.Join(attrs.GetTags(), " "),
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
