package data

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

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
			// Toplists are per-group rankings: keep the last value of every
			// series (one per group) so the renderer can draw bars.
			if w.Type == "toplist" {
				w.Items = seriesLastValues(mq)
			}
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

// seriesLastValues extracts each series' scope label and last point, largest
// first, capped — the toplist shape.
func seriesLastValues(mq datadogV1.MetricsQueryResponse) []WidgetItem {
	const maxItems = 6
	var items []WidgetItem
	for _, s := range mq.GetSeries() {
		label := s.GetScope()
		if label == "" || label == "*" {
			label = s.GetExpression()
		}
		pts := s.GetPointlist()
		for i := len(pts) - 1; i >= 0; i-- {
			if len(pts[i]) > 1 && pts[i][1] != nil {
				items = append(items, WidgetItem{Label: label, Value: *pts[i][1]})
				break
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Value > items[j].Value })
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	return items
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
