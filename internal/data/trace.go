package data

import (
	"fmt"
	"sort"
)

// maxTraceSpans caps how many spans one trace reconstruction fetches — a
// runaway trace can't fan out unbounded, same discipline as the rest.
const maxTraceSpans = 100

// buildTrace links spans (with absolute OffsetUs = start UnixMicro) into a
// DFS-ordered tree: roots first, children under parents ordered by start,
// depth assigned, and every offset normalized relative to the earliest span
// so the waterfall starts at 0.
func buildTrace(traceID string, nodes []Span) *TraceView {
	v := &TraceView{TraceID: traceID}
	if len(nodes) == 0 {
		return v
	}
	byID := make(map[string]int, len(nodes))
	for i, n := range nodes {
		byID[n.ID] = i
	}
	minStart := nodes[0].OffsetUs
	for _, n := range nodes {
		if n.OffsetUs < minStart {
			minStart = n.OffsetUs
		}
	}
	children := make(map[string][]int)
	var roots []int
	for i, n := range nodes {
		if _, ok := byID[n.ParentID]; n.ParentID == "" || !ok {
			roots = append(roots, i)
		} else {
			children[n.ParentID] = append(children[n.ParentID], i)
		}
	}
	byStart := func(idxs []int) {
		sort.SliceStable(idxs, func(a, b int) bool { return nodes[idxs[a]].OffsetUs < nodes[idxs[b]].OffsetUs })
	}
	byStart(roots)

	var walk func(i, depth int)
	walk = func(i, depth int) {
		n := nodes[i]
		n.Depth = depth
		n.OffsetUs -= minStart
		if end := n.OffsetUs + n.DurationUs; end > v.TotalUs {
			v.TotalUs = end
		}
		v.Spans = append(v.Spans, n)
		kids := children[n.ID]
		byStart(kids)
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return v
}

// FormatDuration renders a microsecond duration compactly (820µs, 12.3ms, 1.4s).
func FormatDuration(us int64) string {
	switch {
	case us <= 0:
		return "0"
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.2fs", float64(us)/1_000_000)
	}
}
