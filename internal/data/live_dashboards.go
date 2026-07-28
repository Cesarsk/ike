package data

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// Dashboard renders a dashboard: fetch its definition, flatten the widget
// tree, and fetch a sparkline for each metric widget (bounded). Widgets we
// can't chart (log streams, notes, formula-only queries) still appear, with
// a note instead of a sparkline.
func (l *Live) Dashboard(ctx context.Context, id string, window time.Duration) (*DashboardView, error) {
	if window <= 0 {
		window = time.Hour
	}
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
	var specs []widgetSpec
	collectWidgets(m["widgets"], &specs)

	metricAPI := datadogV1.NewMetricsApi(l.client)
	v2API := datadogV2.NewMetricsApi(l.client)
	from := time.Now().Add(-window).Unix()
	to := time.Now().Unix()
	fetched := 0
	for i := range specs {
		w := &specs[i].Widget
		hasV2 := len(specs[i].queriesJSON) > 0
		if w.Query == "" && !hasV2 {
			continue // Note already explains this widget type
		}
		if fetched >= MaxDashWidgets {
			w.Note = "chart budget reached — open in Datadog (o)"
			view.Truncated = true
			continue
		}
		fetched++
		// Classic single-q widgets stay on the cheap v1 metrics query.
		if w.Query != "" {
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
			continue
		}
		// Formula widgets (metrics, spans, logs, RUM sources) run through the
		// v2 query API — the same engine the web dashboard uses, so formulas
		// evaluate and non-metric sources chart. The dashboard definition's
		// queries[]/formulas[] JSON is the v2 request's own shape, passed
		// through nearly verbatim.
		l.queryWidgetV2(ctx, v2API, w, specs[i].queriesJSON, specs[i].formulasJSON, from, to)
	}
	widgets := make([]Widget, len(specs))
	for i := range specs {
		widgets[i] = specs[i].Widget
	}
	view.Widgets = widgets
	if view.Truncated {
		slog.Warn("dashboard sparklines truncated", "dashboard", id, "cap", MaxDashWidgets)
	}
	return view, nil
}

// widgetSpec pairs a Widget with the raw queries/formulas JSON from its
// definition, for the v2 query pass.
type widgetSpec struct {
	Widget
	queriesJSON  []byte
	formulasJSON []byte
}

// collectWidgets flattens the (possibly nested) widget tree in definition
// order, pulling title, type and the runnable query material from each.
func collectWidgets(node any, out *[]widgetSpec) {
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
		sp := widgetSpec{Widget: Widget{Title: title, Type: typ}}
		sp.Query, sp.queriesJSON, sp.formulasJSON = widgetQuery(def)
		if sp.Query == "" && len(sp.queriesJSON) == 0 {
			sp.Note = widgetTypeNote(typ, def)
		}
		// Layout (free/grid dashboards): x/y/width/height in grid units;
		// absent for ordered layouts (W stays 0 → renderer falls back to flow).
		if lay, ok := wobj["layout"].(map[string]any); ok {
			sp.X = jsonInt(lay["x"])
			sp.Y = jsonInt(lay["y"])
			sp.W = jsonInt(lay["width"])
			sp.H = jsonInt(lay["height"])
		}
		*out = append(*out, sp)
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

// widgetQuery extracts the runnable query material from a widget definition:
// a classic requests[].q (charted via the cheap v1 metrics query), or the raw
// queries[]/formulas[] JSON of a formula widget (charted via the v2 query API,
// which evaluates formulas and supports spans/logs/RUM sources).
func widgetQuery(def map[string]any) (string, []byte, []byte) {
	reqs := def["requests"]
	// query_value widgets sometimes have requests as an object, not a list.
	var reqList []any
	switch r := reqs.(type) {
	case []any:
		reqList = r
	case map[string]any:
		reqList = []any{r}
	default:
		return "", nil, nil
	}
	for _, ri := range reqList {
		req, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		if q, ok := req["q"].(string); ok && q != "" {
			return q, nil, nil
		}
		qs, ok := req["queries"].([]any)
		if !ok || len(qs) == 0 {
			continue
		}
		queriesJSON, err := json.Marshal(qs)
		if err != nil {
			continue
		}
		var formulasJSON []byte
		if fs, ok := req["formulas"].([]any); ok && len(fs) > 0 {
			formulasJSON, _ = json.Marshal(fs)
		}
		return "", queriesJSON, formulasJSON
	}
	return "", nil, nil
}

// widgetTypeNote explains a widget ike can't chart, honestly per type — and
// note widgets show their actual content instead of an apology.
func widgetTypeNote(typ string, def map[string]any) string {
	switch typ {
	case "note", "free_text":
		if c, ok := def["content"].(string); ok && c != "" {
			return c
		}
		if c, ok := def["text"].(string); ok && c != "" {
			return c
		}
		return "empty note"
	case "slo", "slo_list":
		return "SLO widget — attainment lives in :slos"
	case "query_table":
		return "table widget — open in Datadog (o)"
	case "list_stream", "log_stream":
		return "log-stream widget — see :logs"
	case "manage_status":
		return "monitor-summary widget — see :monitors"
	case "event_stream", "event_timeline":
		return "event widget — see :events"
	case "trace_service":
		return "APM service widget — see :services"
	case "hostmap":
		return "host-map widget — see :hosts"
	case "iframe", "image":
		return typ + " widget — open in Datadog (o)"
	}
	return "no metric query to chart — open in Datadog (o)"
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

// queryWidgetV2 charts a formula widget through the v2 query API. query_value
// widgets run the scalar endpoint (the exact number the web tile shows);
// everything else runs the timeseries endpoint. Failures degrade to a note —
// never an error for the whole dashboard.
func (l *Live) queryWidgetV2(ctx context.Context, api *datadogV2.MetricsApi, w *Widget, queriesJSON, formulasJSON []byte, from, to int64) {
	w.Query = displayQuery(queriesJSON, formulasJSON)
	var formulas []datadogV2.QueryFormula
	if len(formulasJSON) > 0 {
		if err := json.Unmarshal(formulasJSON, &formulas); err != nil {
			formulas = nil
		}
	}
	fromMs, toMs := from*1000, to*1000

	if w.Type == "query_value" {
		var qs []datadogV2.ScalarQuery
		if err := json.Unmarshal(queriesJSON, &qs); err != nil || len(qs) == 0 {
			w.Note = "unsupported query shape — open in Datadog (o)"
			return
		}
		attrs := datadogV2.NewScalarFormulaRequestAttributes(fromMs, qs, toMs)
		attrs.Formulas = formulas
		body := datadogV2.NewScalarFormulaQueryRequest(*datadogV2.NewScalarFormulaRequest(
			*attrs, datadogV2.SCALARFORMULAREQUESTTYPE_SCALAR_REQUEST))
		resp, httpresp, err := api.QueryScalarData(ctx, *body)
		l.track(httpresp)
		if err != nil {
			w.Note = "query failed"
			slog.Debug("widget scalar query failed", "title", w.Title, "err", err)
			return
		}
		respData := resp.GetData()
		respAttrs := respData.GetAttributes()
		for _, col := range respAttrs.GetColumns() {
			if col.DataScalarColumn == nil {
				continue
			}
			for _, v := range col.DataScalarColumn.GetValues() {
				if v != nil {
					w.Last = *v
					w.HasData = true
					return
				}
			}
		}
		w.Note = "no data in last 1h"
		return
	}

	var qs []datadogV2.TimeseriesQuery
	if err := json.Unmarshal(queriesJSON, &qs); err != nil || len(qs) == 0 {
		w.Note = "unsupported query shape — open in Datadog (o)"
		return
	}
	attrs := datadogV2.NewTimeseriesFormulaRequestAttributes(fromMs, qs, toMs)
	attrs.Formulas = formulas
	body := datadogV2.NewTimeseriesFormulaQueryRequest(*datadogV2.NewTimeseriesFormulaRequest(
		*attrs, datadogV2.TIMESERIESFORMULAREQUESTTYPE_TIMESERIES_REQUEST))
	resp, httpresp, err := api.QueryTimeseriesData(ctx, *body)
	l.track(httpresp)
	if err != nil {
		w.Note = "query failed"
		slog.Debug("widget timeseries query failed", "title", w.Title, "err", err)
		return
	}
	respData := resp.GetData()
	respAttrs := respData.GetAttributes()
	values := respAttrs.GetValues()
	series := respAttrs.GetSeries()
	if len(values) == 0 {
		w.Note = "no data in last 1h"
		return
	}
	w.Spark = densify(values[0])
	if len(w.Spark) > 0 {
		w.Last = w.Spark[len(w.Spark)-1]
		w.HasData = true
	} else {
		w.Note = "no data in last 1h"
		return
	}
	if w.Type == "toplist" {
		const maxItems = 6
		for i, v := range values {
			pts := densify(v)
			if len(pts) == 0 {
				continue
			}
			label := "total"
			if i < len(series) {
				if tags := series[i].GetGroupTags(); len(tags) > 0 {
					label = strings.Join(tags, ",")
				}
			}
			w.Items = append(w.Items, WidgetItem{Label: label, Value: pts[len(pts)-1]})
		}
		sort.SliceStable(w.Items, func(i, j int) bool { return w.Items[i].Value > w.Items[j].Value })
		if len(w.Items) > maxItems {
			w.Items = w.Items[:maxItems]
		}
	}
}

// densify drops nil points from a v2 series (sparse buckets are nil, not 0).
func densify(pts []*float64) []float64 {
	out := make([]float64, 0, len(pts))
	for _, p := range pts {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// displayQuery is the human-readable query line for a formula widget: a real
// formula expression when there is one, else the referenced sub-query's text.
// Dashboards attach a trivial formula ("query1") even to single-query widgets
// — that name is useless to show, so bare references resolve to the query.
func displayQuery(queriesJSON, formulasJSON []byte) string {
	var qs []map[string]any
	_ = json.Unmarshal(queriesJSON, &qs)
	queryText := func(q map[string]any) string {
		if t, ok := q["query"].(string); ok && t != "" {
			return t
		}
		if search, ok := q["search"].(map[string]any); ok {
			if t, ok := search["query"].(string); ok && t != "" {
				return t
			}
		}
		if ds, ok := q["data_source"].(string); ok {
			return ds + " query"
		}
		return ""
	}
	byName := map[string]string{}
	for _, q := range qs {
		if n, ok := q["name"].(string); ok && n != "" {
			byName[n] = queryText(q)
		}
	}
	if len(formulasJSON) > 0 {
		var fs []map[string]any
		if json.Unmarshal(formulasJSON, &fs) == nil && len(fs) > 0 {
			if f, ok := fs[0]["formula"].(string); ok && f != "" {
				if t, bare := byName[f]; bare && t != "" {
					return t // "query1" → the query it names
				}
				return f
			}
		}
	}
	if len(qs) > 0 {
		return queryText(qs[0])
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
