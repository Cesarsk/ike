package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// The service-health rollup (h on :services): everything ike knows about one
// service on a single screen — its monitors, SLOs, error-tracking issues,
// recent deploy events, and the owning team's on-call — composed entirely
// from the per-resource caches, so the panel costs at most one fetch per
// stale section rather than a new API surface.

// healthData is one gather's result; sections that failed carry their error
// and render as unavailable instead of failing the panel.
type healthData struct {
	monitors []data.Row
	slos     []data.Row
	errors   []data.Row
	deploys  []data.Row
	team     string   // owning team handle (from the monitors' team: tag)
	onCall   []string // who is on call for that team right now
	teamRow  data.Row // resolved on-call team row ("" ID = not resolved)
	errs     map[string]string
}

// showServiceHealth opens the health panel for a service row (h on :services).
func (a *App) showServiceHealth(row data.Row) {
	svc := row.ID
	if svc == "" {
		return
	}
	a.pushNav()
	a.healthRow = row
	a.healthData = nil
	a.healthView.SetTitle(fmt.Sprintf(" Health · %s ", svc))
	a.healthView.SetText(a.theme.recolor("\n  [gray]gathering service health…")).ScrollToBeginning()
	a.showPage("health")
	prov := a.providerFor(row)
	cur := a.current
	go func() {
		d := a.gatherServiceHealth(prov, svc)
		a.QueueUpdateDraw(func() {
			if a.page != "health" || a.current != cur || a.healthRow.ID != svc {
				return // navigated away
			}
			a.healthData = d
			a.healthView.SetText(a.theme.recolor(renderServiceHealth(svc, d))).ScrollToBeginning()
		})
	}()
}

// gatherServiceHealth pulls each section concurrently through the shared
// caches. Tag-based sections filter client-side (works for live and demo
// alike); errors and deploys are service-scoped queries.
func (a *App) gatherServiceHealth(prov *data.Cached, svc string) *healthData {
	d := &healthData{errs: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	fetch := func(section, resKey, query string, keep func(data.Row) bool, into *[]data.Row) {
		defer wg.Done()
		res, ok := data.ResourceByAlias(resKey)
		if !ok {
			return
		}
		rows, _, _, err := prov.Fetch(context.Background(), res, query, "", false)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			d.errs[section] = err.Error()
			return
		}
		for _, r := range rows {
			if keep == nil || keep(r) {
				*into = append(*into, r)
			}
		}
	}
	hasServiceTag := func(cell int) func(data.Row) bool {
		return func(r data.Row) bool {
			return len(r.Cells) > cell && strings.Contains(r.Cells[cell], "service:"+svc)
		}
	}
	wg.Add(4)
	go fetch("monitors", "monitors", "", hasServiceTag(5), &d.monitors)
	go fetch("slos", "slos", "", hasServiceTag(4), &d.slos)
	go fetch("errors", "errors", "service:"+svc, nil, &d.errors)
	// Demo ignores the events query, so keep the client-side match too.
	go fetch("deploys", "events", "service:"+svc, func(r data.Row) bool {
		return strings.Contains(strings.Join(r.Cells, " "), svc)
	}, &d.deploys)
	wg.Wait()

	// Owner: the first team: tag on the service's monitors, resolved to the
	// on-call team (same walk as P on a monitor). Best-effort throughout.
	for _, m := range d.monitors {
		if d.team = teamTag(m); d.team != "" {
			break
		}
	}
	if d.team == "" {
		return d
	}
	oncallRes, ok := data.ResourceByAlias("oncall")
	if !ok {
		return d
	}
	teams, _, _, err := prov.Fetch(context.Background(), oncallRes, "", "", false)
	if err != nil {
		d.errs["oncall"] = err.Error()
		return d
	}
	for _, t := range teams {
		if (len(t.Cells) > 1 && strings.EqualFold(t.Cells[1], d.team)) ||
			(len(t.Cells) > 0 && strings.EqualFold(t.Cells[0], d.team)) {
			d.teamRow = t
			break
		}
	}
	if d.teamRow.ID == "" {
		return d
	}
	if det, err := prov.TeamOnCall(context.Background(), d.teamRow.ID); err == nil {
		for _, r := range det.OnCall {
			d.onCall = append(d.onCall, r.Name)
		}
	}
	return d
}

// renderServiceHealth draws the one-screen rollup.
func renderServiceHealth(svc string, d *healthData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n [orange::b]%s[-:-:-]", tview.Escape(svc))
	if d.team != "" {
		fmt.Fprintf(&b, "  [gray]owner[-] [white]%s[-]", tview.Escape(d.team))
		if len(d.onCall) > 0 {
			fmt.Fprintf(&b, "  [gray]on call now[-] [white]%s[-]", tview.Escape(strings.Join(d.onCall, ", ")))
		}
	}
	b.WriteString("\n")

	section := func(title string, rows []data.Row, errKey string, line func(data.Row) string, empty string) {
		fmt.Fprintf(&b, "\n [orange]%s[-]\n", title)
		if e, ok := d.errs[errKey]; ok {
			fmt.Fprintf(&b, "   [gray](unavailable: %s)[-]\n", tview.Escape(firstLineN(e, 60)))
			return
		}
		if len(rows) == 0 {
			fmt.Fprintf(&b, "   [gray]%s[-]\n", empty)
			return
		}
		const cap = 8
		for i, r := range rows {
			if i == cap {
				fmt.Fprintf(&b, "   [gray]… %d more[-]\n", len(rows)-cap)
				break
			}
			b.WriteString("   " + line(r) + "\n")
		}
	}

	section("MONITORS", d.monitors, "monitors", func(r data.Row) string {
		state, name := cellAt(r, 0), cellAt(r, 2)
		color := "lightgray"
		switch strings.ToLower(state) {
		case "alert":
			color = "red"
		case "warn":
			color = "yellow"
		case "ok":
			color = "lightgreen"
		case "no data":
			color = "gray"
		}
		return fmt.Sprintf("[%s]%-8s[-] %s", color, tview.Escape(state), tview.Escape(name))
	}, "no monitors tagged service:"+svc)

	section("SLOS", d.slos, "slos", func(r data.Row) string {
		return fmt.Sprintf("[white]%s[-]  [gray]%s · target %s / %s[-]",
			tview.Escape(cellAt(r, 0)), tview.Escape(cellAt(r, 1)), tview.Escape(cellAt(r, 2)), tview.Escape(cellAt(r, 3)))
	}, "no SLOs tagged service:"+svc)

	section("ERRORS (24h)", d.errors, "errors", func(r data.Row) string {
		state := cellAt(r, 1)
		color := "lightgray"
		switch strings.ToLower(state) {
		case "open":
			color = "red"
		case "acknowledged":
			color = "yellow"
		}
		return fmt.Sprintf("[%s]%-12s[-] %6s× %s  [gray]%s[-]",
			color, tview.Escape(state), tview.Escape(cellAt(r, 2)), tview.Escape(cellAt(r, 3)), tview.Escape(firstLineN(cellAt(r, 4), 48)))
	}, "no error-tracking issues")

	section("RECENT EVENTS", d.deploys, "deploys", func(r data.Row) string {
		return fmt.Sprintf("[white]%s[-]  %s  [gray]%s[-]",
			tview.Escape(cellAt(r, 0)), tview.Escape(cellAt(r, 1)), tview.Escape(firstLineN(cellAt(r, 3), 56)))
	}, "no events mentioning "+svc)

	b.WriteString("\n [gray]<l> error logs · <t> traces · <P> page owner · <o> open · <ctrl-r> refresh · <esc> back[-]\n")
	return b.String()
}

// firstLineN is s's first line, capped to n runes.
func firstLineN(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// healthPageOwner pages the service's owning team from the panel, reusing the
// monitor-owner confirm (who-it-wakes included).
func (a *App) healthPageOwner() {
	d := a.healthData
	if d == nil || d.teamRow.ID == "" {
		a.flash("no owning on-call team resolved for this service", true)
		return
	}
	a.confirmPageOwner(d.teamRow, d.team, "service "+a.healthRow.ID, strings.Join(d.onCall, ", "))
}
