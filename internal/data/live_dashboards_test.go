package data

import "testing"

func TestWidgetQueryExtraction(t *testing.T) {
	// Classic requests[].q → the v1 path.
	q, qj, fj := widgetQuery(map[string]any{"requests": []any{map[string]any{"q": "avg:a{*}"}}})
	if q != "avg:a{*}" || qj != nil || fj != nil {
		t.Errorf("classic q: got %q/%v/%v", q, qj, fj)
	}
	// Formula widget (spans source + formula) → raw JSON for the v2 path.
	q, qj, fj = widgetQuery(map[string]any{"requests": []any{map[string]any{
		"queries": []any{
			map[string]any{"data_source": "spans", "name": "query1", "compute": map[string]any{"aggregation": "count"}},
			map[string]any{"data_source": "metrics", "name": "query2", "query": "sum:b{*}"},
		},
		"formulas": []any{map[string]any{"formula": "query1 / query2 * 100"}},
	}}})
	if q != "" || len(qj) == 0 || len(fj) == 0 {
		t.Errorf("formula widget should carry raw v2 JSON: q=%q qj=%d fj=%d", q, len(qj), len(fj))
	}
	// No runnable material at all.
	if q, qj, _ := widgetQuery(map[string]any{}); q != "" || qj != nil {
		t.Errorf("empty def: got %q/%v", q, qj)
	}
}

func TestDisplayQuery(t *testing.T) {
	// The formula expression wins when present.
	got := displayQuery([]byte(`[{"data_source":"spans","query":"x"}]`), []byte(`[{"formula":"query1 * 2"}]`))
	if got != "query1 * 2" {
		t.Errorf("formula display: %q", got)
	}
	// Else the first sub-query's text.
	got = displayQuery([]byte(`[{"data_source":"metrics","query":"sum:b{*}"}]`), nil)
	if got != "sum:b{*}" {
		t.Errorf("query display: %q", got)
	}
	// Events-style search.query.
	got = displayQuery([]byte(`[{"data_source":"spans","search":{"query":"service:x"}}]`), nil)
	if got != "service:x" {
		t.Errorf("search display: %q", got)
	}
	// A bare "query1" formula is a reference, not an expression: resolve it
	// to the query it names instead of showing the useless token.
	got = displayQuery(
		[]byte(`[{"data_source":"metrics","name":"query1","query":"sum:mem{*} by {kube_service}"}]`),
		[]byte(`[{"formula":"query1"}]`))
	if got != "sum:mem{*} by {kube_service}" {
		t.Errorf("bare formula should resolve to the query text: %q", got)
	}
}

func TestWidgetTypeNote(t *testing.T) {
	if got := widgetTypeNote("note", map[string]any{"content": "hello"}); got != "hello" {
		t.Errorf("note content: %q", got)
	}
	if got := widgetTypeNote("slo", nil); got == "" || got == "no metric query to chart — open in Datadog (o)" {
		t.Errorf("slo should have a typed note: %q", got)
	}
	if got := widgetTypeNote("mystery_widget", nil); got != "no metric query to chart — open in Datadog (o)" {
		t.Errorf("unknown type fallback: %q", got)
	}
}

func TestDensify(t *testing.T) {
	v1, v3 := 1.0, 3.0
	got := densify([]*float64{&v1, nil, &v3, nil})
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("densify: %v", got)
	}
}
