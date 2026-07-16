package data

import "testing"

func TestBuildTraceTreeOrder(t *testing.T) {
	// root(0) → a(10) → b(15); root → c(20). Absolute starts; buildTrace
	// normalizes to relative offsets and assigns depth in DFS order.
	nodes := []Span{
		{ID: "b", ParentID: "a", Service: "db", OffsetUs: 1015, DurationUs: 5},
		{ID: "root", ParentID: "", Service: "ingress", OffsetUs: 1000, DurationUs: 100},
		{ID: "c", ParentID: "root", Service: "cache", OffsetUs: 1020, DurationUs: 5},
		{ID: "a", ParentID: "root", Service: "app", OffsetUs: 1010, DurationUs: 20},
	}
	v := buildTrace("t1", nodes)

	wantOrder := []struct {
		svc    string
		depth  int
		offset int64
	}{
		{"ingress", 0, 0}, // root, offset normalized to 0
		{"app", 1, 10},    // child of root, starts +10
		{"db", 2, 15},     // child of app
		{"cache", 1, 20},  // child of root, after app subtree
	}
	if len(v.Spans) != len(wantOrder) {
		t.Fatalf("got %d spans, want %d", len(v.Spans), len(wantOrder))
	}
	for i, w := range wantOrder {
		s := v.Spans[i]
		if s.Service != w.svc || s.Depth != w.depth || s.OffsetUs != w.offset {
			t.Errorf("span %d = {%s d%d @%d}, want {%s d%d @%d}", i, s.Service, s.Depth, s.OffsetUs, w.svc, w.depth, w.offset)
		}
	}
	if v.TotalUs != 100 { // ingress spans 0..100
		t.Errorf("TotalUs = %d, want 100", v.TotalUs)
	}
}

func TestBuildTraceOrphanIsRoot(t *testing.T) {
	// A span whose parent isn't in the set is treated as a root, not dropped.
	v := buildTrace("t2", []Span{
		{ID: "x", ParentID: "missing", Service: "svc", OffsetUs: 500, DurationUs: 10},
	})
	if len(v.Spans) != 1 || v.Spans[0].Depth != 0 || v.Spans[0].OffsetUs != 0 {
		t.Errorf("orphan not promoted to root: %+v", v.Spans)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{0: "0", 820: "820µs", 12300: "12.3ms", 1_400_000: "1.40s"}
	for us, want := range cases {
		if got := FormatDuration(us); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", us, got, want)
		}
	}
}

func TestTraceIDFromAttrs(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]interface{}
		want  string
	}{
		{"top-level string", map[string]interface{}{"trace_id": "abc"}, "abc"},
		{"dotted key", map[string]interface{}{"dd.trace_id": "def"}, "def"},
		{"nested dd map", map[string]interface{}{"dd": map[string]interface{}{"trace_id": "ghi"}}, "ghi"},
		{"numeric id", map[string]interface{}{"trace_id": float64(123456789)}, "123456789"},
		{"absent", map[string]interface{}{"service": "x"}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := traceIDFromAttrs(c.attrs); got != c.want {
			t.Errorf("%s: traceIDFromAttrs = %q, want %q", c.name, got, c.want)
		}
	}
}
