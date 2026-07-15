package data

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// Demo is an offline Provider with plausible SRE-flavoured data so the TUI
// can be exercised (and demoed) without Datadog credentials. States jitter
// a little on every refresh to make auto-refresh visible.
type Demo struct {
	site  string
	mu    sync.Mutex
	rnd   *rand.Rand
	mons  []demoMonitor
	incSt map[string]string // incident id → state, mutated by SetIncidentState
}

type demoMonitor struct {
	id    int
	name  string
	typ   string
	state string
	prio  string
	tags  string
}

func NewDemo(site string) *Demo {
	d := &Demo{site: site, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
	names := []struct {
		name, typ, prio, tags string
	}{
		{"EKS node CPU high on {cluster}", "metric alert", "P2", "team:sre,service:eks"},
		{"Kong data plane 5xx rate", "metric alert", "P1", "team:sre,service:kong-proxy"},
		{"ArgoCD application out of sync", "service check", "P3", "team:sre,service:argocd"},
		{"RDS free storage below 20%", "metric alert", "P2", "team:sre,service:rds"},
		{"Payments API p99 latency > 800ms", "metric alert", "P1", "team:payments,service:payments-api"},
		{"Vault sealed", "service check", "P1", "team:sre,service:vault"},
		{"Istio ingress error budget burn", "metric alert", "P2", "team:sre,service:istio"},
		{"Kafka consumer lag > 10k", "metric alert", "P2", "team:platform,service:kafka"},
		{"Node not ready in prod", "service check", "P1", "team:sre,service:eks"},
		{"Certificate expiring in 14 days", "event alert", "P3", "team:sre,service:cert-manager"},
		{"Trading engine order rejects", "metric alert", "P1", "team:trading,service:trading-engine"},
		{"S3 4xx on document bucket", "metric alert", "P4", "team:backend,service:s3"},
		{"Redis memory fragmentation", "metric alert", "P3", "team:sre,service:redis"},
		{"CoreDNS latency", "metric alert", "P3", "team:sre,service:coredns"},
		{"Synthetic: login journey failing", "synthetics alert", "P1", "team:frontend,service:onboarding"},
		{"Datadog agent not reporting", "service check", "P2", "team:sre,service:datadog-agent"},
		{"WAF blocked requests spike", "metric alert", "P2", "team:security,service:waf"},
		{"Backup job missed schedule", "event alert", "P2", "team:sre,service:velero"},
	}
	states := []string{"OK", "OK", "OK", "OK", "Alert", "Warn", "No Data", "OK", "Alert", "OK", "OK", "Warn", "OK", "OK", "Alert", "No Data", "OK", "Warn"}
	clusters := []string{"prod-1", "stage-2", "dev-1"}
	for i, n := range names {
		name := strings.ReplaceAll(n.name, "{cluster}", clusters[i%len(clusters)])
		d.mons = append(d.mons, demoMonitor{
			id: 4200 + i, name: name, typ: n.typ, state: states[i], prio: n.prio, tags: n.tags,
		})
	}
	return d
}

func (d *Demo) Mode() string { return "demo" }
func (d *Demo) Site() string { return d.site }

func (d *Demo) Budget() []string {
	return []string{
		"monitors 973/1000 per 10s",
		"logs_search 287/300 per 3600s",
		"slo_list 98/100 per 60s",
	}
}

func (d *Demo) Fetch(_ context.Context, key, query, timeRange string) ([]Row, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch key {
	case "monitors":
		return d.monitors(), nil
	case "incidents":
		return d.incidents(), nil
	case "slos":
		return d.slos(), nil
	case "logs":
		return d.logs(query, timeRange), nil
	case "dashboards":
		return d.dashboards(), nil
	}
	return nil, fmt.Errorf("unknown resource %q", key)
}

// Dashboard synthesizes a renderable dashboard with sparkline data so the
// widget view is demoable and e2e-testable offline.
func (d *Demo) Dashboard(_ context.Context, id string) (*DashboardView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	widgets := []struct {
		title, typ, query string
		base, amp         float64
		data              bool
	}{
		{"Request rate", "timeseries", "sum:kong.requests{*}.as_rate()", 1200, 300, true},
		{"5xx rate", "timeseries", "sum:kong.http.5xx{*}.as_rate()", 12, 20, true},
		{"p99 latency (ms)", "query_value", "p99:trace.http.request.duration{*}", 640, 120, true},
		{"CPU %", "timeseries", "avg:system.cpu.user{*}", 55, 30, true},
		{"Pod restarts", "toplist", "sum:kubernetes.containers.restarts{*}", 3, 4, true},
		{"Deploy notes", "note", "", 0, 0, false},
	}
	view := &DashboardView{Title: "SRE Overview (" + id + ")"}
	for _, w := range widgets {
		wd := Widget{Title: w.title, Type: w.typ, Query: w.query}
		if w.data {
			pts := make([]float64, 30)
			for i := range pts {
				pts[i] = w.base + w.amp*math.Sin(float64(i)/4) + float64(d.rnd.Intn(int(w.amp)+1))
			}
			wd.Spark = pts
			wd.Last = pts[len(pts)-1]
			wd.HasData = true
		} else {
			wd.Note = "note widget — no metric to chart"
		}
		view.Widgets = append(view.Widgets, wd)
	}
	return view, nil
}

// FetchDetail mirrors the live behavior (monitors, dashboards and incidents
// have richer detail objects) so the on-demand upgrade is demoable and
// testable offline.
func (d *Demo) FetchDetail(_ context.Context, key, id string) (any, error) {
	switch key {
	case "monitors", "dashboards", "incidents":
		return map[string]any{
			"id":          id,
			"resource":    key,
			"full_object": true,
			"note":        "demo: in live mode this is the complete object fetched on demand (widgets, options, timeline …)",
		}, nil
	}
	return nil, nil
}

func (d *Demo) monitors() []Row {
	// Jitter: occasionally flip a monitor state so refreshes are visible.
	if i := d.rnd.Intn(len(d.mons) * 3); i < len(d.mons) {
		switch d.mons[i].state {
		case "OK":
			d.mons[i].state = "Warn"
		case "Warn":
			d.mons[i].state = "Alert"
		default:
			d.mons[i].state = "OK"
		}
	}
	rows := make([]Row, 0, len(d.mons))
	for _, m := range d.mons {
		var logQ []string
		for _, tag := range strings.Split(m.tags, ",") {
			if strings.HasPrefix(tag, "service:") || strings.HasPrefix(tag, "env:") {
				logQ = append(logQ, tag)
			}
		}
		rows = append(rows, Row{
			ID:       fmt.Sprintf("%d", m.id),
			LogQuery: strings.Join(logQ, " "),
			Cells:    []string{m.state, m.name, m.typ, m.prio, m.tags},
			Raw: map[string]any{
				"id": m.id, "name": m.name, "type": m.typ, "overall_state": m.state,
				"priority": m.prio, "tags": strings.Split(m.tags, ","),
				"query":   "avg(last_5m):avg:system.cpu.user{...} > 90",
				"message": "Runbook: https://wiki.example.com/runbooks/" + strings.ReplaceAll(strings.ToLower(m.name), " ", "-"),
			},
			URL: fmt.Sprintf("%s/monitors/%d", WebBase(d.site), m.id),
		})
	}
	SortMonitors(rows)
	return rows
}

func (d *Demo) incidents() []Row {
	incs := []struct {
		id, sev, state, title string
		impact                bool
		age                   time.Duration
	}{
		{"IR-142", "SEV-1", "active", "Kong data plane returning 5xx in prod", true, 42 * time.Minute},
		{"IR-141", "SEV-2", "stable", "Elevated latency on payments API", true, 3 * time.Hour},
		{"IR-139", "SEV-3", "resolved", "ArgoCD sync storm after chart bump", false, 26 * time.Hour},
		{"IR-138", "SEV-2", "resolved", "RDS failover in stage", false, 2 * 24 * time.Hour},
		{"IR-135", "SEV-4", "resolved", "Flaky synthetic on login journey", false, 4 * 24 * time.Hour},
	}
	rows := make([]Row, 0, len(incs))
	for _, in := range incs {
		state := in.state
		if s, ok := d.incSt[in.id]; ok {
			state = s // reflect an in-session SetIncidentState change
		}
		created := time.Now().Add(-in.age)
		impact := ""
		if in.impact {
			impact = "customer"
		}
		rows = append(rows, Row{
			ID:    in.id,
			Cells: []string{in.id, in.sev, state, in.title, impact, created.Format("2006-01-02 15:04")},
			Raw: map[string]any{
				"public_id": in.id, "severity": in.sev, "state": state,
				"title": in.title, "customer_impacted": in.impact, "created": created.Format(time.RFC3339),
			},
			URL: WebBase(d.site) + "/incidents/" + strings.TrimPrefix(in.id, "IR-"),
		})
	}
	return rows
}

// SetIncidentState records a state change in demo mode so the incidents view
// reflects it, mirroring the live write path.
func (d *Demo) SetIncidentState(_ context.Context, id, state string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.incSt == nil {
		d.incSt = map[string]string{}
	}
	d.incSt[id] = state
	return nil
}

func (d *Demo) slos() []Row {
	slos := []struct{ name, typ, target, tf, tags string }{
		{"Kong availability", "metric", "99.90%", "30d", "team:sre,service:kong-proxy"},
		{"Payments API latency < 500ms", "metric", "99.50%", "30d", "team:payments"},
		{"EKS control plane availability", "monitor", "99.95%", "90d", "team:sre"},
		{"Trading order success rate", "metric", "99.90%", "7d", "team:trading"},
		{"Onboarding flow success", "monitor", "99.00%", "30d", "team:frontend"},
		{"Vault availability", "monitor", "99.99%", "90d", "team:sre"},
		{"Log pipeline freshness", "metric", "99.50%", "7d", "team:platform"},
		{"ArgoCD sync success", "monitor", "99.00%", "30d", "team:sre"},
	}
	rows := make([]Row, 0, len(slos))
	for i, s := range slos {
		rows = append(rows, Row{
			ID:    fmt.Sprintf("slo-%d", i),
			Cells: []string{s.name, s.typ, s.target, s.tf, s.tags},
			Raw:   map[string]any{"name": s.name, "type": s.typ, "target": s.target, "timeframe": s.tf},
			URL:   WebBase(d.site) + "/slo",
		})
	}
	return rows
}

func (d *Demo) logs(query, timeRange string) []Row {
	// Spread demo timestamps across the requested window so changing the
	// time range is visible offline (best-effort parse of "now-<n><unit>").
	windowSec := 900
	if secs, ok := rangeSeconds(timeRange); ok {
		windowSec = secs
	}
	services := []struct{ svc, host string }{
		{"kong-proxy", "ip-10-1-2-11"},
		{"payments-api", "ip-10-1-4-23"},
		{"argocd-repo-server", "ip-10-1-3-8"},
		{"trading-engine", "ip-10-1-5-2"},
		{"vault", "ip-10-1-2-30"},
	}
	msgs := []struct{ status, msg string }{
		{"info", "request completed status=200 path=/api/v1/orders latency=123ms"},
		{"error", "upstream timeout status=504 upstream=payments-api attempt=2"},
		{"warn", "retrying connection to kafka broker-2 backoff=4s"},
		{"info", "reconciliation finished app=platform-workloads revision=f3a9c1"},
		{"error", "failed to renew lease: context deadline exceeded"},
		{"info", "healthcheck ok component=scheduler"},
		{"warn", "certificate expires in 13 days cn=*.example.com"},
		{"error", "panic recovered in handler path=/api/v1/quotes"},
	}
	// Token-aware query handling so drill-down queries like
	// "service:kong-proxy status:error" behave like the real search API.
	var statusFilter, svcFilter string
	var textToks []string
	for _, tok := range strings.Fields(strings.ToLower(strings.TrimSpace(query))) {
		switch {
		case tok == "*":
		case strings.HasPrefix(tok, "status:"):
			statusFilter = strings.TrimPrefix(tok, "status:")
		case strings.HasPrefix(tok, "service:"):
			svcFilter = strings.TrimPrefix(tok, "service:")
		case strings.HasPrefix(tok, "env:"):
			// demo data is single-env; accept and ignore
		default:
			textToks = append(textToks, tok)
		}
	}
	var rows []Row
	for i := 0; i < 60; i++ {
		s := services[d.rnd.Intn(len(services))]
		m := msgs[d.rnd.Intn(len(msgs))]
		if statusFilter != "" && m.status != statusFilter {
			continue
		}
		if svcFilter != "" && s.svc != svcFilter {
			continue
		}
		line := strings.ToLower(s.svc + " " + m.msg)
		skip := false
		for _, tok := range textToks {
			if !strings.Contains(line, tok) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		ts := time.Now().Add(-time.Duration(d.rnd.Intn(windowSec)) * time.Second)
		stamp := ts.Format("15:04:05")
		if windowSec > 24*3600 {
			stamp = ts.Format("01-02 15:04") // multi-day window: show the date
		}
		rows = append(rows, Row{
			ID:    fmt.Sprintf("log-%d", i),
			Cells: []string{stamp, m.status, s.svc, s.host, m.msg},
			Raw: map[string]any{
				"timestamp": ts.Format(time.RFC3339), "status": m.status,
				"service": s.svc, "host": s.host, "message": m.msg,
				"tags": []string{"env:prod", "team:sre"},
			},
			URL: WebBase(d.site) + "/logs?query=service:" + s.svc,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cells[0] > rows[j].Cells[0] }) // newest first
	return rows
}

func (d *Demo) dashboards() []Row {
	dashs := []struct{ title, layout, author string }{
		{"SRE Overview", "ordered", "alice"},
		{"Kong Gateway", "free", "alice"},
		{"EKS Clusters", "ordered", "platform-bot"},
		{"Payments Golden Signals", "ordered", "payments-team"},
		{"Trading Engine", "free", "trading-team"},
		{"RDS Fleet", "ordered", "sre-bot"},
		{"Istio Mesh", "ordered", "platform-bot"},
		{"Cost Overview", "ordered", "finops"},
	}
	rows := make([]Row, 0, len(dashs))
	for i, ds := range dashs {
		mod := time.Now().Add(-time.Duration(i*7) * time.Hour)
		id := fmt.Sprintf("abc-%03d", i)
		rows = append(rows, Row{
			ID:    id,
			Cells: []string{ds.title, ds.layout, ds.author, mod.Format("2006-01-02 15:04")},
			Raw:   map[string]any{"id": id, "title": ds.title, "layout_type": ds.layout, "author": ds.author},
			URL:   WebBase(d.site) + "/dashboard/" + id,
		})
	}
	return rows
}

// SortMonitors orders rows by state severity (Alert first), then name.
func SortMonitors(rows []Row) {
	rank := map[string]int{"Alert": 0, "Warn": 1, "No Data": 2, "Unknown": 3, "OK": 4}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			ri, ok1 := rank[rows[j].Cells[0]]
			rj, ok2 := rank[rows[j-1].Cells[0]]
			if !ok1 {
				ri = 3
			}
			if !ok2 {
				rj = 3
			}
			if ri < rj || (ri == rj && rows[j].Cells[1] < rows[j-1].Cells[1]) {
				rows[j], rows[j-1] = rows[j-1], rows[j]
			} else {
				break
			}
		}
	}
}
