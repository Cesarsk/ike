package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// maxDepDepth bounds the transitive expansion so a dense graph stays readable.
const maxDepDepth = 8

// depAdjacency rebuilds the call graph from the :deps rows (each row's Raw
// carries its edges) — the graph page renders from data already fetched.
func depAdjacency(rows []data.Row) (calls, calledBy map[string][]string) {
	calls, calledBy = map[string][]string{}, map[string][]string{}
	for _, r := range rows {
		raw, ok := r.Raw.(map[string]any)
		if !ok {
			continue
		}
		calls[r.ID] = rawStrings(raw["calls"])
		calledBy[r.ID] = rawStrings(raw["called_by"])
	}
	return calls, calledBy
}

// rawStrings coerces a Raw edge list ([]string live, []any after a JSON
// round-trip) into strings.
func rawStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// renderDepFocus draws one service's neighborhood: who calls it (upstream),
// then its transitive downstream tree.
func renderDepFocus(svc string, calls, calledBy map[string][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n [orange::b]%s[-:-:-]\n\n", tview.Escape(svc))
	up := append([]string(nil), calledBy[svc]...)
	sort.Strings(up)
	b.WriteString(" [gray]called by[-]\n")
	if len(up) == 0 {
		b.WriteString("   [gray](nothing — an entry point)[-]\n")
	}
	for _, u := range up {
		fmt.Fprintf(&b, "   [aqua]%s[-] [gray]─▶[-] %s\n", tview.Escape(u), tview.Escape(svc))
	}
	b.WriteString("\n [gray]calls[-]\n")
	if len(calls[svc]) == 0 {
		b.WriteString("   [gray](nothing — a leaf)[-]\n")
	} else {
		expanded := map[string]bool{}
		writeDepTree(&b, svc, calls, map[string]bool{svc: true}, expanded, "  ", 0)
	}
	b.WriteString("\n [gray]<j/k> scroll · <g> whole graph · <esc> back[-]\n")
	return b.String()
}

// renderDepForest draws the whole env graph as a forest rooted at the
// services nothing else calls.
func renderDepForest(calls, calledBy map[string][]string) string {
	var roots []string
	for svc := range calls {
		if len(calledBy[svc]) == 0 {
			roots = append(roots, svc)
		}
	}
	sort.Strings(roots)
	var b strings.Builder
	fmt.Fprintf(&b, "\n [orange::b]dependency graph[-:-:-]  [gray]%d services, roots first · ↺ cycle · … shown above[-]\n", len(calls))
	if len(roots) == 0 && len(calls) > 0 {
		// Everything is called by something: fully cyclic. Fall back to all.
		for svc := range calls {
			roots = append(roots, svc)
		}
		sort.Strings(roots)
	}
	expanded := map[string]bool{}
	for _, r := range roots {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  [aqua::b]%s[-:-:-]\n", tview.Escape(r))
		writeDepTree(&b, r, calls, map[string]bool{r: true}, expanded, "  ", 0)
		expanded[r] = true
	}
	b.WriteString("\n [gray]<j/k> scroll · <esc> back[-]\n")
	return b.String()
}

// writeDepTree writes svc's callees as a box-drawing tree. onPath guards
// cycles (marked ↺); expanded dedupes — a service's subtree is drawn once,
// later occurrences show "…".
func writeDepTree(b *strings.Builder, svc string, calls map[string][]string, onPath, expanded map[string]bool, indent string, depth int) {
	children := append([]string(nil), calls[svc]...)
	sort.Strings(children)
	for i, c := range children {
		branch, cont := "├─▶", "│  "
		if i == len(children)-1 {
			branch, cont = "└─▶", "   "
		}
		switch {
		case onPath[c]:
			fmt.Fprintf(b, "%s[gray]%s[-] %s [red]↺[-]\n", indent, branch, tview.Escape(c))
		case expanded[c] && len(calls[c]) > 0:
			fmt.Fprintf(b, "%s[gray]%s[-] %s [gray]…[-]\n", indent, branch, tview.Escape(c))
		case depth >= maxDepDepth:
			fmt.Fprintf(b, "%s[gray]%s[-] %s [gray](depth cap)[-]\n", indent, branch, tview.Escape(c))
		default:
			fmt.Fprintf(b, "%s[gray]%s[-] %s\n", indent, branch, tview.Escape(c))
			onPath[c] = true
			writeDepTree(b, c, calls, onPath, expanded, indent+cont, depth+1)
			delete(onPath, c)
			expanded[c] = true
		}
	}
}

// showDepGraph opens the graph page: focused on a service, or the whole
// forest when svc is empty.
func (a *App) showDepGraph(svc string) {
	if !a.renderDepGraphPage(svc) {
		return
	}
	a.pushNav()
	a.showPage("depgraph")
}

// renderDepGraphPage draws the graph into the pane (no navigation); false
// means there was nothing to draw.
func (a *App) renderDepGraphPage(svc string) bool {
	calls, calledBy := depAdjacency(a.rows)
	if len(calls) == 0 {
		a.flash("no dependency data loaded", true)
		return false
	}
	body := ""
	if svc == "" {
		a.depGraph.SetTitle(" Dependency graph ")
		body = renderDepForest(calls, calledBy)
	} else {
		a.depGraph.SetTitle(fmt.Sprintf(" Dependencies · %s ", svc))
		body = renderDepFocus(svc, calls, calledBy)
	}
	a.depGraph.SetText(a.theme.recolor(body)).ScrollToBeginning()
	return true
}
