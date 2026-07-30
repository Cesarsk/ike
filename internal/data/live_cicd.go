package data

import (
	"context"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// cicd lists CI Visibility pipeline executions (newest first). The event's
// payload is a free-form attribute map, so fields are dug out defensively.
func (l *Live) cicd(ctx context.Context, query, timeRange string) ([]Row, error) {
	if strings.TrimSpace(query) == "" || strings.TrimSpace(query) == "*" {
		query = "ci_level:pipeline"
	} else if !strings.Contains(query, "ci_level:") {
		query = "ci_level:pipeline " + query
	}
	secs := 86400
	if s, ok := rangeSeconds(timeRange); ok {
		secs = s
	}
	now := time.Now()
	opts := datadogV2.NewListCIAppPipelineEventsOptionalParameters().
		WithFilterQuery(query).
		WithFilterFrom(now.Add(-time.Duration(secs) * time.Second)).
		WithFilterTo(now).
		WithSort(datadogV2.CIAPPSORT_TIMESTAMP_DESCENDING).
		WithPageLimit(100)
	resp, httpresp, err := datadogV2.NewCIVisibilityPipelinesApi(l.client).ListCIAppPipelineEvents(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("ci pipelines", err)
	}
	events := resp.GetData()
	rows := make([]Row, 0, len(events))
	for _, ev := range events {
		attrs := ev.GetAttributes()
		m := attrs.GetAttributes()
		ci := rumMap(m, "ci")
		pipe := rumStr(rumMap(ci, "pipeline"), "name")
		status := rumStr(ci, "status")
		if status == "" {
			status = rumStr(m, "status")
		}
		branch := rumStr(rumMap(m, "git"), "branch")
		provider := rumStr(rumMap(ci, "provider"), "name")
		dur := ""
		if d, ok := m["duration"].(float64); ok && d > 0 {
			dur = time.Duration(d).Truncate(time.Second).String() // nanoseconds
		}
		when := ""
		if ts, ok := m["end"].(float64); ok && ts > 0 {
			when = msToTime(ts).Local().Format("01-02 15:04")
		}
		url := rumStr(rumMap(ci, "pipeline"), "url")
		rows = append(rows, Row{
			ID:    ev.GetId(),
			Cells: []string{when, status, pipe, branch, dur, provider, strings.Join(attrs.GetTags(), " ")},
			Raw:   ev,
			URL:   url,
		})
	}
	return rows, nil
}

// msToTime converts an epoch that may be seconds, milliseconds or nanoseconds
// (CI event payloads are inconsistent) into a time.
func msToTime(v float64) time.Time {
	switch {
	case v > 1e16: // nanoseconds
		return time.Unix(0, int64(v))
	case v > 1e12: // milliseconds
		return time.UnixMilli(int64(v))
	default: // seconds
		return time.Unix(int64(v), 0)
	}
}
