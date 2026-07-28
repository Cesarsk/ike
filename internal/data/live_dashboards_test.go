package data

import "testing"

func TestWidgetQueryExtraction(t *testing.T) {
	// Classic requests[].q.
	q, n := widgetQuery(map[string]any{"requests": []any{map[string]any{"q": "avg:a{*}"}}})
	if q != "avg:a{*}" || n != 1 {
		t.Errorf("classic q: got %q/%d", q, n)
	}
	// Multi-query formula widget: first metrics sub-query, ofN reported.
	q, n = widgetQuery(map[string]any{"requests": []any{map[string]any{
		"queries": []any{
			map[string]any{"data_source": "logs", "query": "status:error"},
			map[string]any{"data_source": "metrics", "query": "sum:b{*}"},
			map[string]any{"data_source": "metrics", "query": "sum:c{*}"},
		},
	}}})
	if q != "sum:b{*}" || n != 3 {
		t.Errorf("formula: got %q/%d, want sum:b{*}/3", q, n)
	}
	// No runnable query at all.
	if q, _ := widgetQuery(map[string]any{}); q != "" {
		t.Errorf("empty def: got %q", q)
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
