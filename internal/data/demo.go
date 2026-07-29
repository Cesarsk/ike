package data

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Demo is an offline Provider with plausible SRE-flavoured data so the TUI
// can be exercised (and demoed) without Datadog credentials. States jitter
// a little on every refresh to make auto-refresh visible.
// Demo satisfies Provider.
var _ Provider = (*Demo)(nil)

type Demo struct {
	site     string
	mu       sync.Mutex
	rnd      *rand.Rand
	mons     []demoMonitor
	incSt    map[string]string // incident id → state, mutated by SetIncidentField
	incSev   map[string]string // incident id → severity, mutated by SetIncidentField
	dtGone   map[string]bool   // downtime id → cancelled, mutated by CancelDowntime
	incTodos map[string][]Todo // incident id → to-dos, mutated by the to-do panel
	hostMute map[string]bool   // host name → muted, mutated by SetHostMute
	errSt    map[string]string // issue id → state, mutated by SetIssueState
}

type demoMonitor struct {
	id    int
	name  string
	typ   string
	state string
	prio  string
	tags  string
	muted bool
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

// Cost synthesizes a plausible Datadog bill so the :cost view is demoable
// offline: the current month mid-statement (estimate + projection) plus
// deterministic closed-month history with mild variation, and an optional
// two-sub-org split mirroring the API's "sub-org" view.
func (d *Demo) Cost(_ context.Context, o CostOptions) (*CostView, error) {
	products := []CostLine{
		{Product: "infra_hosts", Total: 4820, Projected: 9100},
		{Product: "logs_indexed", Total: 3110, Projected: 6250},
		{Product: "apm_hosts", Total: 2040, Projected: 3900},
		{Product: "custom_metrics", Total: 980, Projected: 1800},
		{Product: "rum_sessions", Total: 610, Projected: 1200},
		{Product: "synthetics", Total: 240, Projected: 470},
	}
	months := o.Months
	if months < 1 {
		months = 1
	}
	if months > maxCostMonths {
		months = maxCostMonths
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	v := &CostView{
		OrgName:  "demo (" + d.site + ")",
		Currency: "USD",
		URL:      WebBase(d.site) + "/billing/usage",
	}
	for i := 0; i < months; i++ {
		m := CostMonth{Month: monthStart.AddDate(0, -i, 0).Format("2006-01"), Current: i == 0}
		// Closed months bill the full projected figure, with a per-product
		// deterministic wobble so the trend and the month-over-month deltas
		// have a shape, plus one planted spike so the anomaly flag shows.
		for pi, p := range products {
			total, projected := p.Total, p.Projected
			if i > 0 {
				scale := 1.0 + 0.05*float64((i*7+pi*3)%5-2)
				total, projected = p.Projected*scale, 0
				if i == 1 && p.Product == "logs_indexed" {
					total *= 1.6
				}
			}
			m.Lines = append(m.Lines, demoCostLines(p.Product, total, projected, o.SubOrgs)...)
		}
		for _, l := range m.Lines {
			m.Total += l.Total
			m.Projected += l.Projected
		}
		if i > 0 {
			m.Projected = 0
		}
		v.Months = append(v.Months, m)
	}
	return v, nil
}

// demoCostLines returns one summary line, or a 70/30 prod/staging split in
// sub-org view.
func demoCostLines(product string, total, projected float64, subOrgs bool) []CostLine {
	if !subOrgs {
		return []CostLine{{Product: product, Total: total, Projected: projected}}
	}
	return []CostLine{
		{Org: "demo-prod", Product: product, Total: total * 0.7, Projected: projected * 0.7},
		{Org: "demo-staging", Product: product, Total: total * 0.3, Projected: projected * 0.3},
	}
}

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
	case "traces":
		return d.spans(query), nil
	case "services":
		return d.services(), nil
	case "events":
		return d.events(), nil
	case "rum":
		return d.rum(query), nil
	case "synthetics":
		return d.synthetics(), nil
	case "downtimes":
		return d.downtimes(), nil
	case "teams":
		return d.teams(), nil
	case "oncall":
		return d.oncallTeams(), nil
	case "security":
		return d.securitySignals(query), nil
	case "notebooks":
		return d.notebooks(), nil
	case "hosts":
		return d.hosts(), nil
	case "errors":
		return d.errorIssues(query), nil
	case "containers":
		return d.containers(query), nil
	case "processes":
		return d.processes(query), nil
	case "audit":
		return d.audit(query), nil
	case "deps":
		return d.deps(query), nil
	case "cases":
		return d.cases(query), nil
	case "cicd":
		return d.cicd(query), nil
	case "fleet":
		return d.fleet(query), nil
	}
	return nil, fmt.Errorf("unknown resource %q", key)
}

// cases backs the :cases view offline.
func (d *Demo) cases(query string) []Row {
	list := []struct{ key, status, prio, title, created string }{
		{"CASE-101", "IN PROGRESS", "P2", "Recurring 5xx spikes on kong-proxy during deploys", "2026-07-28 14:02"},
		{"CASE-100", "OPEN", "P3", "Vault lease renewals occasionally time out", "2026-07-27 09:41"},
		{"CASE-97", "OPEN", "P4", "Review noisy disk-space monitors on kafka brokers", "2026-07-24 16:20"},
		{"CASE-92", "CLOSED", "P3", "Postgres connection pool exhaustion on payments-db", "2026-07-19 11:05"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(list))
	for i, c := range list {
		hay := strings.ToLower(c.key + " " + c.status + " " + c.title)
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		rows = append(rows, Row{
			ID:    fmt.Sprintf("case-%d", i),
			Cells: []string{c.key, c.status, c.prio, c.title, c.created},
			Raw:   map[string]any{"key": c.key},
			URL:   WebBase(d.site) + "/cases",
		})
	}
	return rows
}

// cicd backs the :cicd view offline.
func (d *Demo) cicd(query string) []Row {
	list := []struct{ when, status, pipe, branch, dur, provider string }{
		{"10:44", "success", "payments-api", "main", "6m20s", "gitlab"},
		{"10:31", "error", "trading-engine", "feat/order-router", "4m02s", "gitlab"},
		{"10:12", "success", "platform-workloads", "main", "2m45s", "gitlab"},
		{"09:58", "success", "onboarding-web", "main", "8m11s", "gitlab"},
		{"09:40", "canceled", "payments-api", "feat/retry-budget", "1m03s", "gitlab"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(list))
	for i, p := range list {
		hay := strings.ToLower(p.pipe + " " + p.branch + " " + p.status)
		if q != "" && !strings.Contains(hay, strings.ToLower(q)) {
			continue
		}
		rows = append(rows, Row{
			ID:    fmt.Sprintf("pipe-%d", i),
			Cells: []string{p.when, p.status, p.pipe, p.branch, p.dur, p.provider},
			Raw:   map[string]any{"pipeline": p.pipe},
			URL:   WebBase(d.site) + "/ci/pipelines",
		})
	}
	return rows
}

// fleet backs the :fleet view offline (oldest agent versions first).
func (d *Demo) fleet(query string) []Row {
	list := []struct{ host, ver, cluster, os, envs, restarted string }{
		{"bastion.mgmt", "7.52.1", "", "ubuntu-22.04", "mgmt", "90d"},
		{"kafka-3.platform", "7.61.0", "", "ubuntu-22.04", "prod", "20d"},
		{"ip-10-0-1-14.eks-prod", "7.66.1", "payments-prod", "amazon-linux-2023", "prod", "6d"},
		{"ip-10-0-2-31.eks-prod", "7.66.1", "payments-prod", "amazon-linux-2023", "prod", "6d"},
		{"ip-10-1-4-7.eks-stage", "7.66.1", "trading-stage", "amazon-linux-2023", "stage", "2d"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(list))
	for _, a := range list {
		hay := strings.ToLower(a.host + " " + a.cluster + " " + a.envs + " " + a.ver)
		if q != "" && !strings.Contains(hay, strings.TrimPrefix(strings.TrimPrefix(q, "env:"), "cluster_name:")) {
			continue
		}
		rows = append(rows, Row{
			ID:    a.host,
			Cells: []string{a.host, a.ver, a.cluster, a.os, a.envs, a.restarted},
			Raw:   map[string]any{"agent_version": a.ver},
			URL:   WebBase(d.site) + "/fleet",
		})
	}
	return rows
}

// deps backs the :deps view offline: a small believable call graph.
func (d *Demo) deps(query string) []Row {
	env := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "env:"))
	if env == "" || env == "*" {
		env = "prod"
	}
	graph := map[string][]string{
		"kong-proxy":     {"payments-api", "trading-engine", "onboarding-api"},
		"payments-api":   {"payments-db", "redis", "vault"},
		"trading-engine": {"kafka", "payments-api"},
		"onboarding-api": {"payments-db", "redis"},
		"payments-db":    {},
		"redis":          {},
		"vault":          {},
		"kafka":          {},
	}
	calledBy := map[string][]string{}
	for svc, calls := range graph {
		for _, c := range calls {
			calledBy[c] = append(calledBy[c], svc)
		}
	}
	rows := make([]Row, 0, len(graph))
	for svc, calls := range graph {
		up := calledBy[svc]
		sort.Strings(up)
		down := append([]string(nil), calls...)
		sort.Strings(down)
		rows = append(rows, Row{
			ID:    svc,
			Cells: []string{svc, fmt.Sprintf("%d", len(down)), fmt.Sprintf("%d", len(up)), strings.Join(down, " ")},
			Raw:   map[string]any{"env": env, "calls": down, "called_by": up},
			URL:   WebBase(d.site) + "/apm/map?env=" + env,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ci := len(graph[rows[i].ID]) + len(calledBy[rows[i].ID])
		cj := len(graph[rows[j].ID]) + len(calledBy[rows[j].ID])
		if ci != cj {
			return ci > cj
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// processes backs the :processes view offline.
func (d *Demo) processes(query string) []Row {
	procs := []struct {
		cmd, user, host, pid, ppid, started, tags string
	}{
		{"kong -c /usr/local/kong/kong.conf", "kong", "kong-dp-1.prod", "3812", "1", "6d", "team:sre,env:prod"},
		{"postgres: payments payments-db idle", "postgres", "rds-payments-prod", "912", "884", "12d", "team:payments,env:prod"},
		{"java -Xmx4g -jar kafka.jar", "kafka", "kafka-3.platform", "2201", "1", "20d", "team:platform,env:prod"},
		{"redis-server *:6379", "redis", "redis-1.prod", "1544", "1", "9d", "team:sre,env:prod"},
		{"vault server -config=/etc/vault.hcl", "vault", "vault-2.prod", "1102", "1", "15d", "team:sre,env:prod"},
		{"python3 tokenizer-worker.py --queue high", "app", "ip-10-0-2-31.eks-prod", "40331", "40100", "2h", "team:payments,env:prod"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(procs))
	for i, p := range procs {
		hay := strings.ToLower(p.cmd + " " + p.host + " " + p.tags)
		if q != "" && !strings.Contains(hay, strings.TrimPrefix(q, "host:")) {
			continue
		}
		rows = append(rows, Row{
			ID:    fmt.Sprintf("proc-%d", i),
			Cells: []string{p.cmd, p.user, p.host, p.pid, p.ppid, p.started, p.tags},
			Raw:   map[string]any{"cmdline": p.cmd},
			URL:   WebBase(d.site) + "/process",
		})
	}
	return rows
}

// audit backs the :audit view offline.
func (d *Demo) audit(query string) []Row {
	events := []struct {
		ts, service, action, user, msg string
	}{
		{"10:41", "monitor", "monitor.modified", "alice@example.com", "Monitor 'Kong 5xx rate' thresholds changed"},
		{"10:12", "dashboard", "dashboard.created", "bob@example.com", "Dashboard 'Golden Signals' created"},
		{"09:58", "authentication", "user.login", "alice@example.com", "User logged in via SAML"},
		{"09:30", "api_key", "api_key.created", "carol@example.com", "API key 'ci-deploy' created"},
		{"08:47", "monitor", "monitor.muted", "bob@example.com", "Monitor 'EKS node NotReady' muted for 1h"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(events))
	for i, e := range events {
		hay := strings.ToLower(e.service + " " + e.action + " " + e.user + " " + e.msg)
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		rows = append(rows, Row{
			ID:    fmt.Sprintf("audit-%d", i),
			Cells: []string{e.ts, e.service, e.action, e.user, e.msg},
			Raw:   map[string]any{"action": e.action},
			URL:   WebBase(d.site) + "/audit-trail",
		})
	}
	return rows
}

// securitySignals synthesizes a few Cloud SIEM signals so the :security view
// is demoable offline.
func (d *Demo) securitySignals(query string) []Row {
	sigs := []struct {
		id, sev, title, tags string
	}{
		{"sig-1", "critical", "Multiple failed root logins from a single IP", "security:attack source:cloudtrail severity:critical"},
		{"sig-2", "high", "IAM policy granting full admin created", "security:posture source:cloudtrail severity:high"},
		{"sig-3", "medium", "Container running as privileged", "security:posture source:k8s severity:medium"},
		{"sig-4", "low", "New SSH key added to an account", "security:activity source:cloudtrail severity:low"},
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(sigs))
	for i, s := range sigs {
		if q != "" && !strings.Contains(strings.ToLower(s.title+" "+s.tags), q) {
			continue
		}
		ts := time.Now().Add(-time.Duration(i*37) * time.Minute)
		rows = append(rows, Row{
			ID:    s.id,
			Cells: []string{ts.Format("2006-01-02 15:04"), s.sev, s.title, s.tags},
			Raw:   map[string]any{"id": s.id, "severity": s.sev, "title": s.title, "tags": s.tags},
			URL:   WebBase(d.site) + "/security",
		})
	}
	return rows
}

// demoNotebooks backs the :notebooks list; demoNotebookBody holds a body per
// notebook for the drill-in.
var demoNotebooks = []struct {
	id, name, author, status, body string
}{
	{"101", "Runbook: Payments API latency", "Alice Ng", "published",
		"# Payments API latency\n\nWhen p99 latency alerts:\n\n1. Check the APM service page for the slow endpoint.\n2. Look at the RDS connection pool saturation.\n3. If the pool is exhausted, scale the read replicas.\n\n[timeseries chart]\n\nEscalate to the payments on-call if latency stays above 800ms for 15m."},
	{"102", "Postmortem: 2026-06 Kong outage", "Bob Ito", "published",
		"# Kong outage postmortem\n\n**Impact:** 22 minutes of elevated 5xx on the public API.\n\n**Root cause:** a config rollout dropped an upstream.\n\n**Action items:** add a canary check to the rollout pipeline."},
	{"103", "Draft: On-call handbook", "Carol Diaz", "draft",
		"# On-call handbook (draft)\n\nTODO: fill in the escalation matrix and the paging etiquette."},
}

func (d *Demo) notebooks() []Row {
	rows := make([]Row, 0, len(demoNotebooks))
	for i, nb := range demoNotebooks {
		mod := time.Now().Add(-time.Duration(i*19) * time.Hour)
		rows = append(rows, Row{
			ID:    nb.id,
			Cells: []string{nb.name, nb.author, nb.status, mod.Format("2006-01-02 15:04")},
			URL:   WebBase(d.site) + "/notebook/" + nb.id,
		})
	}
	return rows
}

// demoErrors backs the :errors view; triage state is overlaid from d.errSt.
var demoErrors = []struct {
	id, last, state, count, typ, msg, service string
}{
	{"err-1", "2m", "open", "18342", "NullPointerException", "Cannot invoke Order.getId() because order is null", "payments-api"},
	{"err-2", "7m", "open", "4021", "TimeoutError (crash)", "upstream request timeout after 30s", "kong-proxy"},
	{"err-3", "41m", "acknowledged", "913", "ValueError", "invalid literal for settlement amount", "trading-engine"},
	{"err-4", "3h", "open", "377", "TypeError", "cannot read properties of undefined (reading token)", "onboarding"},
	{"err-5", "2d", "resolved", "12055", "ConnectionResetError", "connection reset by peer", "redis"},
}

// errorIssues runs under Fetch's lock (do not re-lock d.mu — not reentrant).
func (d *Demo) errorIssues(query string) []Row {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "*" {
		q = ""
	}
	rows := make([]Row, 0, len(demoErrors))
	for _, e := range demoErrors {
		if q != "" && !strings.Contains(strings.ToLower(e.typ+" "+e.msg+" service:"+e.service), q) {
			continue
		}
		state := e.state
		if s, ok := d.errSt[e.id]; ok {
			state = strings.ToLower(s)
		}
		rows = append(rows, Row{
			ID:       e.id,
			Cells:    []string{e.last, state, e.count, e.typ, e.msg, e.service},
			Raw:      map[string]any{"id": e.id, "type": e.typ, "message": e.msg, "service": e.service, "state": state, "count": e.count},
			URL:      WebBase(d.site) + "/error-tracking?issueId=" + e.id,
			LogQuery: errorLogQuery(e.service),
		})
	}
	return rows
}

// SetIssueState overlays a demo issue's triage state so it survives a reload.
func (d *Demo) SetIssueState(_ context.Context, id, state string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.errSt == nil {
		d.errSt = map[string]string{}
	}
	d.errSt[id] = state
	return nil
}

// demoHosts backs the :hosts view; muted state is overlaid from d.hostMute.
var demoHosts = []struct {
	name, status, apps, cpu, tags string
}{
	{"ip-10-0-1-14.eks-prod", "down", "system,docker,kubernetes", "", "team:sre,env:prod,role:eks-node,kube_cluster_name:payments-prod"},
	{"ip-10-0-2-31.eks-prod", "up", "system,docker,kubernetes", "78%", "team:sre,env:prod,role:eks-node,kube_cluster_name:payments-prod"},
	{"ip-10-0-2-9.eks-prod", "up", "system,docker,kubernetes", "44%", "team:sre,env:prod,role:eks-node,kube_cluster_name:payments-prod"},
	{"rds-payments-prod", "up", "system,postgres", "61%", "team:payments,env:prod,role:rds"},
	{"kong-dp-1.prod", "up", "system,docker,kong", "52%", "team:sre,env:prod,role:kong"},
	{"kafka-3.platform", "up", "system,kafka", "83%", "team:platform,env:prod,role:kafka"},
	{"ip-10-1-4-7.eks-stage", "up", "system,docker,kubernetes", "22%", "team:sre,env:stage,role:eks-node,kube_cluster_name:trading-stage"},
	{"vault-2.prod", "up", "system,vault", "17%", "team:sre,env:prod,role:vault"},
	{"redis-1.prod", "up", "system,redis", "39%", "team:sre,env:prod,role:redis"},
	{"bastion.mgmt", "up", "system", "5%", "team:sre,env:mgmt,role:bastion"},
}

// hosts runs under Fetch's lock (do not re-lock d.mu — it isn't reentrant).
func (d *Demo) hosts() []Row {
	rows := make([]Row, 0, len(demoHosts))
	for _, h := range demoHosts {
		status := h.status
		muted := d.hostMute[h.name] // demo default: none muted until you mute one
		if muted && status != "down" {
			status = "muted"
		}
		last := "just now"
		if h.status == "down" {
			last = "6m"
		}
		awsName := ""
		if strings.HasPrefix(h.name, "ip-") {
			awsName = "i-0" + h.name[3:11]
		}
		rows = append(rows, Row{
			ID: h.name,
			Cells: []string{
				h.name, status, hostCluster(strings.Split(h.tags, ",")),
				h.name, awsName, "", "agent,aws", "linux",
				h.apps, h.cpu, last, h.tags,
			},
			Raw: map[string]any{"muted": muted, "up": h.status != "down"},
			URL: WebBase(d.site) + "/infrastructure?host=" + h.name,
		})
	}
	return rows
}

// demoContainers backs the :containers view.
var demoContainers = []struct {
	name, state, image, ns, cluster, host, started, tags string
}{
	{"payments-api-7d9c", "running", "payments-api:1.42.0", "payments", "eks-prod", "ip-10-0-2-31.eks-prod", "3h", "team:payments,env:prod,service:payments-api,kube_namespace:payments,kube_cluster_name:eks-prod"},
	{"kong-proxy-5f2a", "running", "kong:3.14", "kong", "eks-prod", "kong-dp-1.prod", "2d", "team:sre,env:prod,service:kong-proxy,kube_namespace:kong,kube_cluster_name:eks-prod"},
	{"argocd-repo-8b1", "terminated", "argocd:2.13.2", "argocd", "eks-prod", "ip-10-0-2-9.eks-prod", "5m", "team:sre,env:prod,service:argocd,kube_namespace:argocd,kube_cluster_name:eks-prod"},
	{"trading-engine-3c", "running", "trading-engine:0.9.7", "trading", "eks-prod", "ip-10-0-2-31.eks-prod", "6h", "team:trading,env:prod,service:trading-engine,kube_namespace:trading,kube_cluster_name:eks-prod"},
	{"onboarding-web-a2", "running", "onboarding:2.1.0", "onboarding", "eks-stage", "ip-10-1-4-7.eks-stage", "1d", "team:frontend,env:stage,service:onboarding,kube_namespace:onboarding,kube_cluster_name:eks-stage"},
	{"redis-1", "running", "redis:7.2", "data", "eks-prod", "redis-1.prod", "9d", "team:sre,env:prod,service:redis,kube_namespace:data,kube_cluster_name:eks-prod"},
	{"velero-backup-1", "stopped", "velero:1.13", "velero", "eks-prod", "ip-10-0-2-9.eks-prod", "12h", "team:sre,env:prod,service:velero,kube_namespace:velero,kube_cluster_name:eks-prod"},
	// Lives on the down demo host, so the HOST-ST join has something to show.
	{"checkout-api-9f1", "running", "checkout-api:0.8.2", "payments", "eks-prod", "ip-10-0-1-14.eks-prod", "4h", "team:payments,env:prod,service:checkout-api,kube_namespace:payments,kube_cluster_name:eks-prod"},
}

// containers runs under Fetch's lock (do not re-lock d.mu — not reentrant).
// query is a demo tag filter: a substring match over each container's tags.
func (d *Demo) containers(query string) []Row {
	q := strings.ToLower(strings.TrimSpace(query))
	rows := make([]Row, 0, len(demoContainers))
	for _, c := range demoContainers {
		if q != "" && !strings.Contains(strings.ToLower(c.tags), q) {
			continue
		}
		rows = append(rows, Row{
			ID:       c.name,
			Cells:    []string{c.name, c.state, c.image, c.ns, c.cluster, c.host, "", c.started, c.tags},
			Raw:      map[string]any{"name": c.name, "state": c.state, "image": c.image, "namespace": c.ns, "cluster": c.cluster, "host": c.host, "tags": c.tags},
			URL:      WebBase(d.site) + "/containers?text=" + c.name,
			LogQuery: "container_name:" + c.name,
		})
	}
	return rows
}

// SetHostMute toggles a demo host's muted flag so the change survives a reload.
func (d *Demo) SetHostMute(_ context.Context, host string, mute bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hostMute == nil {
		d.hostMute = map[string]bool{}
	}
	d.hostMute[host] = mute
	return nil
}

// Notebook returns a demo notebook's rendered body.
func (d *Demo) Notebook(_ context.Context, id string) (*NotebookView, error) {
	for _, nb := range demoNotebooks {
		if nb.id == id {
			return &NotebookView{
				Name: nb.name, Author: nb.author, Status: nb.status,
				URL: WebBase(d.site) + "/notebook/" + id, Body: nb.body,
			}, nil
		}
	}
	return &NotebookView{Name: "notebook " + id, URL: WebBase(d.site) + "/notebook/" + id}, nil
}

// demoTeams backs the :teams and :oncall lists. "platform" deliberately has
// no on-call configured, so the on-call drill-in exercises the empty path.
var demoTeams = []struct {
	id, name, handle, desc string
	members                int64
}{
	{"sre", "SRE", "sre", "Reliability, on-call, and platform health", 6},
	{"payments", "Payments", "payments", "Payments API and settlement", 8},
	{"platform", "Platform", "platform", "Shared infra and developer tooling", 5},
}

func (d *Demo) teams() []Row {
	rows := make([]Row, 0, len(demoTeams))
	for _, t := range demoTeams {
		rows = append(rows, Row{
			ID:    t.id,
			Cells: []string{t.name, t.handle, strconv.FormatInt(t.members, 10), t.desc},
			URL:   WebBase(d.site) + "/organization-settings/teams/" + t.handle,
		})
	}
	return rows
}

// demoTeamMembers is a small per-team roster (handle → role) so the :teams
// drill-in is demoable offline.
var demoTeamMembers = map[string][]struct{ handle, role string }{
	"sre":      {{"alice", "admin"}, {"bob", "member"}, {"carol", "member"}, {"sre.oncall", "member"}},
	"payments": {{"dave", "admin"}, {"erin", "member"}},
	"platform": {{"carol", "admin"}, {"dave", "member"}},
}

// TeamMembers returns a team's demo roster, resolving handles against the
// shared demo user list for names.
func (d *Demo) TeamMembers(_ context.Context, teamID string) ([]TeamMember, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []TeamMember
	for _, m := range demoTeamMembers[teamID] {
		tm := TeamMember{Handle: m.handle, Role: m.role, Email: m.handle + "@example.com"}
		for _, u := range demoUsers {
			if u.Handle == m.handle {
				tm.Name = u.Name
			}
		}
		out = append(out, tm)
	}
	return out, nil
}

func (d *Demo) oncallTeams() []Row {
	rows := make([]Row, 0, len(demoTeams))
	for _, t := range demoTeams {
		rows = append(rows, Row{
			ID:    t.id,
			Cells: []string{t.name, t.handle, strconv.FormatInt(t.members, 10)},
			URL:   WebBase(d.site) + "/on-call/teams/" + t.id,
		})
	}
	return rows
}

// TeamOnCall synthesizes a team's on-call state so the panel is demoable
// offline: SRE and Payments have rotations plus an escalation ladder,
// everything else comes back empty (no on-call configured).
func (d *Demo) TeamOnCall(_ context.Context, teamID string) (*OnCallDetail, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	det := &OnCallDetail{URL: WebBase(d.site) + "/on-call/teams/" + teamID}
	r := func(handle string) OnCallResponder {
		for _, u := range demoUsers {
			if u.Handle == handle {
				return OnCallResponder{Name: u.Name, Handle: u.Handle, Email: u.Handle + "@example.com"}
			}
		}
		return OnCallResponder{Name: handle, Handle: handle}
	}
	switch teamID {
	case "sre":
		det.OnCall = []OnCallResponder{r("sre.oncall")}
		det.Escalation = []OnCallLevel{
			{Level: 1, DelayMin: 0, Responders: []OnCallResponder{r("sre.oncall")}},
			{Level: 2, DelayMin: 5, Responders: []OnCallResponder{r("alice"), r("bob")}},
			{Level: 3, DelayMin: 15, Responders: []OnCallResponder{r("carol")}},
		}
	case "payments":
		det.OnCall = []OnCallResponder{r("dave")}
		det.Escalation = []OnCallLevel{
			{Level: 1, DelayMin: 0, Responders: []OnCallResponder{r("dave")}},
			{Level: 2, DelayMin: 10, Responders: []OnCallResponder{r("erin")}},
		}
	}
	return det, nil
}

// PageTeam fakes raising a page in demo mode: no network, a synthetic page id
// so the acknowledge/escalate/resolve lifecycle is exercisable offline.
func (d *Demo) PageTeam(_ context.Context, teamID, _, _, _ string) (string, error) {
	return "demo-page-" + teamID, nil
}

func (d *Demo) AckPage(context.Context, string) error      { return nil }
func (d *Demo) EscalatePage(context.Context, string) error { return nil }
func (d *Demo) ResolvePage(context.Context, string) error  { return nil }

// Dashboard synthesizes a renderable dashboard with sparkline data so the
// widget view is demoable and e2e-testable offline.
func (d *Demo) Dashboard(_ context.Context, id string, _ time.Duration) (*DashboardView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Layout coords (x,y,width,height) mirror a real Datadog grid so the
	// TUI grid renderer has something to arrange: two columns × three rows.
	widgets := []struct {
		title, typ, query string
		base, amp         float64
		data              bool
		x, y, w, h        int
	}{
		{"Request rate", "timeseries", "sum:kong.requests{*}.as_rate()", 1200, 300, true, 0, 0, 6, 2},
		{"5xx rate", "timeseries", "sum:kong.http.5xx{*}.as_rate()", 12, 20, true, 6, 0, 6, 2},
		{"p99 latency (ms)", "query_value", "p99:trace.http.request.duration{*}", 640, 120, true, 0, 2, 4, 2},
		{"CPU %", "timeseries", "avg:system.cpu.user{*}", 55, 30, true, 4, 2, 8, 2},
		{"Pod restarts", "toplist", "sum:kubernetes.containers.restarts{*}", 3, 4, true, 0, 4, 6, 2},
		{"Deploy notes", "note", "", 0, 0, false, 6, 4, 6, 2},
		{"Checkout availability (SLO)", "slo", "", 0, 0, false, 0, 6, 6, 2},
	}
	view := &DashboardView{Title: "SRE Overview (" + id + ")"}
	for _, w := range widgets {
		wd := Widget{Title: w.title, Type: w.typ, Query: w.query, X: w.x, Y: w.y, W: w.w, H: w.h}
		if w.typ == "slo" {
			wd.Query = "Checkout availability"
			wd.Last = 99.97
			wd.HasData = true
			wd.Note = "target 99.90% · 30d · error budget left 70%"
			wd.Spark = make([]float64, 30)
			for i := range wd.Spark {
				wd.Spark[i] = 100 - float64(i)
			}
			view.Widgets = append(view.Widgets, wd)
			continue
		}
		if w.data {
			pts := make([]float64, 30)
			for i := range pts {
				pts[i] = w.base + w.amp*math.Sin(float64(i)/4) + float64(d.rnd.Intn(int(w.amp)+1))
			}
			wd.Spark = pts
			wd.Last = pts[len(pts)-1]
			wd.HasData = true
			if w.typ == "toplist" {
				wd.Items = []WidgetItem{
					{Label: "kong-proxy", Value: 7}, {Label: "payments-api", Value: 4},
					{Label: "redis", Value: 2}, {Label: "coredns", Value: 1},
				}
			}
		} else {
			wd.Note = "Canary at 10% since 09:00 — rollback via `argo rollback` if 5xx grows."
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
	case "incidents":
		// Mirror the live shape: structured war-room summary + People + raw.
		return &IncidentDetail{
			Title:            "Kong data plane returning 5xx in prod",
			Severity:         "SEV-1",
			State:            "active",
			Created:          time.Now().Add(-42 * time.Minute).Format(time.RFC3339),
			CustomerImpacted: true,
			ImpactScope:      "checkout degraded for EU customers",
			Fields:           map[string]string{"root_cause": "config rollout", "services": "kong-proxy, payments-api", "teams": "sre"},
			People: IncidentPeople{
				Commander:  "demo.user",
				DeclaredBy: "alice",
				CreatedBy:  "alice",
				Responders: []string{"bob", "carol"},
			},
			Incident: map[string]any{
				"public_id":   id,
				"resource":    key,
				"full_object": true,
				"note":        "demo: in live mode this is the complete incident fetched on demand (fields, timeline …)",
			},
		}, nil
	case "monitors":
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, m := range d.mons {
			if fmt.Sprintf("%d", m.id) == id {
				return &MonitorDetail{
					Name: m.name, State: m.state, Type: m.typ, Priority: m.prio,
					Query:   "avg(last_5m):avg:system.cpu.user{...} > 90",
					Message: "Runbook: https://wiki.example.com/runbooks/" + strings.ReplaceAll(strings.ToLower(m.name), " ", "-"),
					Tags:    strings.Split(m.tags, ","),
					Monitor: map[string]any{"id": m.id, "full_object": true},
				}, nil
			}
		}
		return nil, nil
	case "dashboards":
		return map[string]any{
			"id":          id,
			"resource":    key,
			"full_object": true,
			"note":        "demo: in live mode this is the complete object fetched on demand (widgets, options, timeline …)",
		}, nil
	case "synthetics":
		results := make([]SynthResult, 6)
		for i := range results {
			results[i] = SynthResult{
				CheckTime: time.Now().Add(-time.Duration(i*10) * time.Minute).Format(time.RFC3339),
				Location:  []string{"aws:eu-central-1", "aws:us-east-1"}[i%2],
				Passed:    i != 0, // newest run failed, the rest passed
			}
		}
		return &SynthDetail{
			Name: "login journey", Type: "browser", Status: "live",
			PassRatePct: 83.3, Results: results,
		}, nil
	case "slos":
		// Deterministic-ish fake attainment so the error-budget detail is
		// demoable: derive from the id so it's stable across refreshes.
		att := 99.0 + float64(len(id)%10)/10.0 // 99.0–99.9
		target := 99.5
		remaining := 100.0
		if att < target {
			remaining = 100 - (target-att)/(100-target)*100
		}
		burndown := make([]float64, 30)
		for i := range burndown {
			// Ease from 100% toward the current remaining budget.
			frac := float64(i) / float64(len(burndown)-1)
			burndown[i] = 100 - (100-remaining)*frac*frac
		}
		return &SLODetail{
			Name: id, Type: "metric", TargetPct: target, TimeframeDays: 30,
			AttainmentPct: att, BudgetRemainingPct: remaining,
			BurnRate: (100 - att) / (100 - target), Burndown: burndown,
		}, nil
	}
	return nil, nil
}

// SetMonitorMute is a no-op success in demo mode (state flips locally so the
// mute/unmute flow is exercisable offline).
func (d *Demo) SetMonitorMute(_ context.Context, id string, mute bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.mons {
		if fmt.Sprintf("%d", d.mons[i].id) == id {
			d.mons[i].muted = mute
		}
	}
	return nil
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
			Muted:    m.muted,
			Cells:    []string{m.state, mutedCell(m.muted), m.name, m.typ, m.prio, m.tags},
			Raw: map[string]any{
				"id": m.id, "name": m.name, "type": m.typ, "overall_state": m.state,
				"priority": m.prio, "tags": strings.Split(m.tags, ","), "muted": m.muted,
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
			state = s // reflect an in-session state change
		}
		sev := in.sev
		if s, ok := d.incSev[in.id]; ok {
			sev = s // reflect an in-session severity change
		}
		created := time.Now().Add(-in.age)
		impact := ""
		if in.impact {
			impact = "customer"
		}
		rows = append(rows, Row{
			ID:    in.id,
			Cells: []string{in.id, sev, state, in.title, impact, created.Format("2006-01-02 15:04")},
			Raw: map[string]any{
				"public_id": in.id, "severity": sev, "state": state,
				"title": in.title, "customer_impacted": in.impact, "created": created.Format(time.RFC3339),
			},
			URL: WebBase(d.site) + "/incidents/" + strings.TrimPrefix(in.id, "IR-"),
		})
	}
	return rows
}

// SetIncidentField records a state or severity change in demo mode so the
// incidents view reflects it, mirroring the live write path.
func (d *Demo) SetIncidentField(_ context.Context, id, field, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch field {
	case "severity":
		if d.incSev == nil {
			d.incSev = map[string]string{}
		}
		d.incSev[id] = value
	default: // "state"
		if d.incSt == nil {
			d.incSt = map[string]string{}
		}
		d.incSt[id] = value
	}
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
	var statusFilter, svcFilter, traceFilter string
	var textToks []string
	for _, tok := range strings.Fields(strings.ToLower(strings.TrimSpace(query))) {
		switch {
		case tok == "*":
		case strings.HasPrefix(tok, "status:"):
			statusFilter = strings.TrimPrefix(tok, "status:")
		case strings.HasPrefix(tok, "service:"):
			svcFilter = strings.TrimPrefix(tok, "service:")
		case strings.HasPrefix(tok, "trace_id:"):
			traceFilter = strings.TrimPrefix(tok, "trace_id:")
		case strings.HasPrefix(tok, "env:"):
			// demo data is single-env; accept and ignore
		default:
			textToks = append(textToks, tok)
		}
	}
	// A trace_id query is the trace → logs drill-down. Synthesize the correlated
	// logs for that trace (one per hop, deepest hop errors) so the drill is
	// never empty in demo mode — mirrors the trace's unified timeline.
	if traceFilter != "" {
		return d.traceLogs(traceFilter)
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
		// Error logs carry a trace id so the log → trace drill-down (t) is
		// demoable; info/warn logs deliberately have none (degrade path).
		traceID := ""
		if m.status == "error" {
			traceID = fmt.Sprintf("demo-trace-%d", 1000+i)
		}
		rows = append(rows, Row{
			ID:      fmt.Sprintf("log-%d", i),
			TraceID: traceID,
			Cells:   []string{stamp, m.status, s.svc, s.host, m.msg},
			Raw: map[string]any{
				"timestamp": ts.Format(time.RFC3339), "status": m.status,
				"service": s.svc, "host": s.host, "message": m.msg,
				"trace_id": traceID, "tags": []string{"env:prod", "team:sre"},
			},
			URL: WebBase(d.site) + "/logs?query=service:" + s.svc,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cells[0] > rows[j].Cells[0] }) // newest first
	return rows
}

// LogContext synthesizes a plausible ±window of log lines around the anchor,
// same service, oldest first, with the anchor line itself in the middle so the
// surrounding-context panel is demoable and e2e-testable offline.
// MetricQuery synthesizes a deterministic series for the :metrics explorer,
// shaped by the query text so different queries look different offline.
func (d *Demo) MetricQuery(_ context.Context, query string, window time.Duration) (*MetricExplorer, error) {
	if window <= 0 {
		window = time.Hour
	}
	seed := 0
	for _, c := range query {
		seed += int(c)
	}
	out := &MetricExplorer{Query: query, Series: 1}
	pts := make([]float64, 40)
	base := float64(20 + seed%60)
	for i := range pts {
		pts[i] = base + 10*math.Sin(float64(i+seed)/5) + float64((i*seed)%7)
	}
	out.Spark = pts
	out.Last = pts[len(pts)-1]
	if strings.Contains(query, "by {") || strings.Contains(query, "by{") {
		out.Series = 4
		out.Items = []WidgetItem{
			{Label: "host:ip-10-0-2-31", Value: out.Last + 12},
			{Label: "host:ip-10-0-2-9", Value: out.Last + 4},
			{Label: "host:kong-dp-1", Value: out.Last},
			{Label: "host:redis-1", Value: out.Last - 9},
		}
	}
	return out, nil
}

// LogCounts mirrors the live aggregation offline: counts demo log rows by
// the facet's column.
func (d *Demo) LogCounts(_ context.Context, query, timeRange, facet string) ([]WidgetItem, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	col := 2 // service
	switch facet {
	case "status":
		col = 1
	case "host":
		col = 3
	}
	counts := map[string]float64{}
	for _, r := range d.logs(query, timeRange) {
		if len(r.Cells) > col {
			counts[r.Cells[col]]++
		}
	}
	items := make([]WidgetItem, 0, len(counts))
	for label, n := range counts {
		items = append(items, WidgetItem{Label: label, Value: n})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].Label < items[j].Label
	})
	return items, nil
}

func (d *Demo) LogContext(_ context.Context, anchor Row, windowSecs int) (*LogContextView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if windowSecs <= 0 {
		windowSecs = 300
	}
	win := time.Duration(windowSecs) * time.Second
	raw, _ := anchor.Raw.(map[string]any)
	svc, _ := raw["service"].(string)
	host, _ := raw["host"].(string)
	anchorMsg, _ := raw["message"].(string)
	anchorStatus, _ := raw["status"].(string)
	anchorTS := time.Now()
	if s, ok := raw["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			anchorTS = t
		}
	}
	ctxMsgs := []struct{ status, msg string }{
		{"info", "request received path=/api/v1/orders"},
		{"info", "cache miss key=order:91422"},
		{"info", "db query SELECT * FROM orders (48ms)"},
		{"warn", "upstream slow: payments-api p95=1.2s"},
		{"warn", "retry scheduled backoff=2s"},
		{"info", "connection pool at 82% capacity"},
		{"info", "request completed status=200 latency=210ms"},
		{"info", "healthcheck ok component=scheduler"},
	}
	n := len(ctxMsgs) + 1 // +1 for the anchor line, placed in the middle
	anchorIdx := n / 2
	rows := make([]Row, 0, n)
	mi := 0
	for i := 0; i < n; i++ {
		frac := float64(i) / float64(n-1)
		ts := anchorTS.Add(time.Duration(-float64(win) + frac*2*float64(win)))
		if i == anchorIdx {
			rows = append(rows, Row{
				ID:    anchor.ID,
				Cells: []string{anchorTS.Local().Format("15:04:05.000"), anchorStatus, svc, host, anchorMsg},
				Raw:   raw,
			})
			continue
		}
		m := ctxMsgs[mi]
		mi++
		rows = append(rows, Row{
			ID:    fmt.Sprintf("%s-ctx-%d", anchor.ID, i),
			Cells: []string{ts.Local().Format("15:04:05.000"), m.status, svc, host, m.msg},
		})
	}
	return &LogContextView{AnchorID: anchor.ID, Service: svc, Host: host, Window: win, Rows: rows}, nil
}

// traceLogs synthesizes the logs correlated to one trace_id: one line per hop
// of the demo trace chain, deepest hop erroring, all stamped with the trace id.
// Deterministic (no jitter) so the trace → logs drill-down is stable to demo
// and to record. Newest-first, matching logs().
func (d *Demo) traceLogs(traceID string) []Row {
	msgs := []string{
		"request received GET /api/v1/orders",
		"handling order lookup id=91422",
		"SELECT * FROM orders WHERE id=91422 (48ms)",
		"quote fetch failed: upstream deadline exceeded",
	}
	hosts := []string{"ip-10-1-2-11", "ip-10-1-4-23", "ip-10-1-4-23", "ip-10-1-5-2"}
	base := time.Now().Add(-90 * time.Second)
	rows := make([]Row, 0, len(demoTraceChain))
	for i, hop := range demoTraceChain {
		status := "info"
		if i == len(demoTraceChain)-1 {
			status = "error"
		}
		ts := base.Add(time.Duration(i*40) * time.Millisecond)
		host := hosts[i%len(hosts)]
		msg := msgs[i%len(msgs)]
		rows = append(rows, Row{
			ID:      fmt.Sprintf("log-%s-%d", traceID, i),
			TraceID: traceID,
			Cells:   []string{ts.Format("15:04:05"), status, hop.svc, host, msg},
			Raw: map[string]any{
				"timestamp": ts.Format(time.RFC3339), "status": status,
				"service": hop.svc, "host": host, "message": msg,
				"trace_id": traceID, "tags": []string{"env:prod", "team:sre"},
			},
			URL: WebBase(d.site) + "/logs?query=trace_id:" + traceID,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cells[0] > rows[j].Cells[0] })
	return rows
}

// demoTraceChain is the service hop path a synthesized trace walks — the
// "where the request comes from" story: ingress → gateway → service → db.
var demoTraceChain = []struct{ svc, res string }{
	{"kong-proxy", "GET /api/v1/orders"},
	{"payments-api", "handler.orders.get"},
	{"payments-api", "postgres.query orders"},
	{"trading-engine", "grpc quote.Get"},
}

func (d *Demo) spans(query string) []Row {
	svcFilter := ""
	for _, tok := range strings.Fields(strings.ToLower(query)) {
		if strings.HasPrefix(tok, "service:") {
			svcFilter = strings.TrimPrefix(tok, "service:")
		}
	}
	var rows []Row
	for i := 0; i < 30; i++ {
		// Cycle the chain and space the timestamps evenly: every service is
		// guaranteed on the first screen of rows, so tests and demos don't
		// depend on how a random shuffle happened to land.
		hop := demoTraceChain[i%len(demoTraceChain)]
		if svcFilter != "" && hop.svc != svcFilter {
			continue
		}
		isErr := d.rnd.Intn(6) == 0
		errMark := ""
		if isErr {
			errMark = "error"
		}
		durUs := int64(500 + d.rnd.Intn(400000))
		ts := time.Now().Add(-time.Duration(i*30) * time.Second)
		tid := fmt.Sprintf("demo-trace-%d", 2000+i)
		rows = append(rows, Row{
			ID:       fmt.Sprintf("span-%d", i),
			TraceID:  tid,
			LogQuery: "trace_id:" + tid,
			Cells:    []string{ts.Format("15:04:05"), hop.svc, hop.res, FormatDuration(durUs), errMark, tid},
			Raw: map[string]any{
				"service": hop.svc, "resource_name": hop.res,
				"trace_id": tid, "duration_us": durUs, "error": isErr,
			},
			URL: WebBase(d.site) + "/apm/trace/" + tid,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cells[0] > rows[j].Cells[0] })
	return rows
}

// MonitorMetric synthesizes a sine-ish series so the monitor detail
// sparkline is demoable offline.
func (d *Demo) MonitorMetric(_ context.Context, id string) (*MetricSeries, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	pts := make([]float64, 30)
	for i := range pts {
		pts[i] = 55 + 30*math.Sin(float64(i)/4) + float64(d.rnd.Intn(10))
	}
	return &MetricSeries{
		Query:  "avg:system.cpu.user{monitor_id:" + id + "}",
		Points: pts, Last: pts[len(pts)-1],
	}, nil
}

// Trace synthesizes a plausible multi-service trace for any id so the
// waterfall drill-down is demoable offline.
func (d *Demo) Trace(_ context.Context, traceID string) (*TraceView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	base := time.Now().UnixMicro()
	var nodes []Span
	offset := int64(0)
	parent := ""
	for i, hop := range demoTraceChain {
		id := fmt.Sprintf("%s-span-%d", traceID, i)
		dur := int64(120000 - i*22000 + d.rnd.Intn(15000)) // outer spans longer
		if dur < 3000 {
			dur = 3000
		}
		nodes = append(nodes, Span{
			ID: id, ParentID: parent, Service: hop.svc, Resource: hop.res,
			OffsetUs: base + offset, DurationUs: dur,
			Error: i == len(demoTraceChain)-1, // deepest hop errored
		})
		parent = id
		offset += int64(4000 + d.rnd.Intn(8000)) // each child starts a bit later
	}
	view := buildTrace(traceID, nodes)
	// Synthesize one log per hop, chronological, so the unified timeline is
	// demoable — the deepest hop logs the error.
	t0 := time.Now().Add(-2 * time.Minute)
	msgs := []string{
		"request received GET /api/v1/orders",
		"handling order lookup id=91422",
		"SELECT * FROM orders WHERE id=91422 (48ms)",
		"quote fetch failed: upstream deadline exceeded",
	}
	for i, hop := range demoTraceChain {
		status := "info"
		if i == len(demoTraceChain)-1 {
			status = "error"
		}
		view.Logs = append(view.Logs, TraceLog{
			Time:    t0.Add(time.Duration(i*40) * time.Millisecond),
			Service: hop.svc, Status: status, Message: msgs[i%len(msgs)],
		})
	}
	return view, nil
}

func (d *Demo) downtimes() []Row {
	dts := []struct {
		status, scope, msg string
		age                time.Duration
	}{
		{"active", "service:payments-api", "Muted during v2.31 rollout", 20 * time.Minute},
		{"active", "*", "Maintenance window — RDS failover drill", 90 * time.Minute},
		{"scheduled", "env:stage", "Nightly batch window", -3 * time.Hour},
		{"ended", "service:kong-proxy", "Post-deploy soak", 26 * time.Hour},
	}
	rows := make([]Row, 0, len(dts))
	for i, dt := range dts {
		id := fmt.Sprintf("dt-%d", i)
		status := dt.status
		if d.dtGone[id] {
			status = "canceled" // reflect an in-session CancelDowntime
		}
		created := time.Now().Add(-dt.age)
		rows = append(rows, Row{
			ID:    id,
			Cells: []string{status, dt.scope, dt.msg, created.Format("2006-01-02 15:04")},
			Raw:   map[string]any{"status": status, "scope": dt.scope, "message": dt.msg},
			URL:   WebBase(d.site) + "/monitors/downtimes",
		})
	}
	return rows
}

// CancelDowntime marks a demo downtime canceled so the view reflects it,
// mirroring the live write path.
func (d *Demo) CancelDowntime(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dtGone == nil {
		d.dtGone = map[string]bool{}
	}
	d.dtGone[id] = true
	return nil
}

// CurrentUser returns a fixed demo user (offline; mirrors the live shape).
func (d *Demo) CurrentUser(_ context.Context) (User, error) {
	return User{ID: "demo-user", Handle: "demo.user"}, nil
}

// SetIncidentCommander is a no-op success in demo mode: the commander doesn't
// surface in the incidents table, so there's nothing to reflect — the write
// path is exercised, it just succeeds.
func (d *Demo) SetIncidentCommander(_ context.Context, _, _ string) error { return nil }

// demoUsers is the offline roster the assignee picker searches.
var demoUsers = []User{
	{ID: "demo-user", Handle: "demo.user", Name: "Demo User"},
	{ID: "u-alice", Handle: "alice", Name: "Alice Ng"},
	{ID: "u-bob", Handle: "bob", Name: "Bob Ito"},
	{ID: "u-carol", Handle: "carol", Name: "Carol Diaz"},
	{ID: "u-dave", Handle: "dave", Name: "Dave Roy"},
	{ID: "u-erin", Handle: "erin", Name: "Erin Poe"},
	{ID: "u-oncall", Handle: "sre.oncall", Name: "SRE On-Call"},
}

// ListUsers filters the demo roster by a case-insensitive substring on
// handle/name (empty query returns all), mirroring the live server-side filter.
func (d *Demo) ListUsers(_ context.Context, query string) ([]User, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]User, len(demoUsers))
		copy(out, demoUsers)
		return out, nil
	}
	var out []User
	for _, u := range demoUsers {
		if strings.Contains(strings.ToLower(u.Handle), q) || strings.Contains(strings.ToLower(u.Name), q) {
			out = append(out, u)
		}
	}
	return out, nil
}

// IncidentTodos returns an incident's to-dos, seeding a plausible pair the
// first time an incident is opened so the panel is demoable.
func (d *Demo) IncidentTodos(_ context.Context, incidentID string) ([]Todo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.incTodos == nil {
		d.incTodos = map[string][]Todo{}
	}
	if _, ok := d.incTodos[incidentID]; !ok {
		d.incTodos[incidentID] = []Todo{
			{ID: incidentID + "-todo-1", Content: "Page the on-call DBA", Assignees: []string{"bob"}, Completed: false},
			{ID: incidentID + "-todo-2", Content: "Post a status-page update", Assignees: []string{"demo.user"}, Completed: true},
		}
	}
	out := make([]Todo, len(d.incTodos[incidentID]))
	copy(out, d.incTodos[incidentID])
	return out, nil
}

// AddIncidentTodo appends a to-do so the demo panel reflects the add.
func (d *Demo) AddIncidentTodo(_ context.Context, incidentID, content, assigneeHandle string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.incTodos == nil {
		d.incTodos = map[string][]Todo{}
	}
	d.incTodos[incidentID] = append(d.incTodos[incidentID], Todo{
		ID:        fmt.Sprintf("%s-todo-%d", incidentID, d.rnd.Intn(1_000_000)),
		Content:   content,
		Assignees: []string{assigneeHandle},
	})
	return nil
}

// SetIncidentTodoCompleted flips a demo to-do's completion so the panel reflects it.
func (d *Demo) SetIncidentTodoCompleted(_ context.Context, incidentID string, todo Todo, done bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.incTodos[incidentID] {
		if d.incTodos[incidentID][i].ID == todo.ID {
			d.incTodos[incidentID][i].Completed = done
		}
	}
	return nil
}

// DeleteIncidentTodo removes a demo to-do so the panel reflects it.
func (d *Demo) DeleteIncidentTodo(_ context.Context, incidentID, todoID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.incTodos[incidentID]
	out := cur[:0:0]
	for _, t := range cur {
		if t.ID != todoID {
			out = append(out, t)
		}
	}
	d.incTodos[incidentID] = out
	return nil
}

func (d *Demo) services() []Row {
	// Sorted names + catalog metadata — mirrors the live service list joined
	// with service-catalog definitions (no per-service stats; the official
	// API doesn't expose them).
	svcs := []struct{ name, team, tier, lifecycle string }{
		{"kong-proxy", "sre", "tier1", "production"},
		{"onboarding-web", "frontend", "tier2", "production"},
		{"payments-api", "payments", "tier1", "production"},
		{"postgres", "", "", ""},
		{"trading-engine", "trading", "tier1", "production"},
		{"vault", "sre", "tier2", "production"},
	}
	rows := make([]Row, 0, len(svcs))
	for _, s := range svcs {
		rows = append(rows, Row{
			ID:    s.name,
			Cells: []string{s.name, s.team, s.tier, s.lifecycle},
			URL:   WebBase(d.site) + "/apm/services/" + s.name,
		})
	}
	return rows
}

func (d *Demo) events() []Row {
	evs := []struct {
		typ, source, title string
		age                time.Duration
	}{
		{"deploy", "gitlab", "Deployed payments-api v2.31.0 to prod", 8 * time.Minute},
		{"error", "monitor", "[Triggered] Kong data plane 5xx rate", 42 * time.Minute},
		{"deploy", "argocd", "Synced platform-workloads → rev f3a9c1", 55 * time.Minute},
		{"info", "user", "@oncall acknowledged IR-142", time.Hour + 3*time.Minute},
		{"warning", "monitor", "[Warn] Payments API p99 latency > 800ms", 90 * time.Minute},
		{"success", "monitor", "[Recovered] RDS failover completed in stage", 2 * time.Hour},
		{"deploy", "gitlab", "Rollback trading-engine to v1.9.4", 3 * time.Hour},
		{"info", "terraform", "Applied 3 changes to kong-dataplane", 4 * time.Hour},
	}
	rows := make([]Row, 0, len(evs))
	for i, e := range evs {
		ts := time.Now().Add(-e.age)
		rows = append(rows, Row{
			ID:    fmt.Sprintf("ev-%d", i),
			Cells: []string{ts.Format("2006-01-02 15:04"), e.typ, e.source, e.title, "env:prod,team:sre"},
			Raw: map[string]any{
				"timestamp": ts.Format(time.RFC3339), "type": e.typ,
				"source": e.source, "title": e.title,
			},
			URL: WebBase(d.site) + "/event/explorer",
		})
	}
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
			Cells: []string{ds.title, ds.layout, ds.author, mod.Format("2006-01-02 15:04"), "team:" + ds.author + " golden-signals for " + ds.title},
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

// IncidentImpacts returns sample impacts so the war-room detail is demoable.
func (d *Demo) IncidentImpacts(_ context.Context, _ string) ([]string, error) {
	return []string{
		"customer: checkout latency > 5s for EU traffic",
		"service: payments-api error rate 12%",
	}, nil
}

// rum synthesizes RUM events (views, actions, errors) so the :rum view is
// demoable offline; the query filters by type:/service: like the real search.
func (d *Demo) rum(query string) []Row {
	var typeFilter string
	for _, tok := range strings.Fields(strings.ToLower(query)) {
		if strings.HasPrefix(tok, "@type:") {
			typeFilter = strings.TrimPrefix(tok, "@type:")
		}
		if strings.HasPrefix(tok, "type:") {
			typeFilter = strings.TrimPrefix(tok, "type:")
		}
	}
	samples := []struct{ typ, app, svc, detail string }{
		{"view", "onboarding-web", "onboarding-web", "/signup/step-2"},
		{"action", "onboarding-web", "onboarding-web", "click on Continue"},
		{"error", "onboarding-web", "onboarding-web", "TypeError: t.user is undefined"},
		{"view", "trading-app", "trading-frontend", "/portfolio"},
		{"action", "trading-app", "trading-frontend", "click on Buy"},
		{"error", "trading-app", "trading-frontend", "NetworkError: /api/v1/quotes timed out"},
		{"view", "onboarding-web", "onboarding-web", "/kyc/documents"},
		{"session", "trading-app", "trading-frontend", "session 34m"},
	}
	var rows []Row
	for i, e := range samples {
		if typeFilter != "" && e.typ != typeFilter {
			continue
		}
		ts := time.Now().Add(-time.Duration(90*i) * time.Second)
		rows = append(rows, Row{
			ID:    fmt.Sprintf("rum-%d", i),
			Cells: []string{ts.Format("15:04:05"), e.typ, e.app, e.svc, e.detail},
			Raw: map[string]any{
				"type": e.typ, "application": e.app, "service": e.svc, "detail": e.detail,
			},
			URL: WebBase(d.site) + "/rum/explorer",
		})
	}
	return rows
}

// synthetics lists sample synthetic tests so the view is demoable offline.
func (d *Demo) synthetics() []Row {
	tests := []struct{ status, name, typ, locs, tags string }{
		{"live", "login journey", "browser", "aws:eu-central-1,aws:us-east-1", "team:frontend,env:prod"},
		{"live", "checkout api", "api", "aws:eu-central-1", "team:payments,env:prod"},
		{"live", "quote latency", "api", "aws:eu-central-1,aws:ap-northeast-1", "team:trading,env:prod"},
		{"paused", "legacy portal", "browser", "aws:us-east-1", "team:frontend,env:stage"},
	}
	rows := make([]Row, 0, len(tests))
	for i, t := range tests {
		id := fmt.Sprintf("syn-%d", i)
		rows = append(rows, Row{
			ID:    id,
			Cells: []string{t.status, t.name, t.typ, t.locs, t.tags},
			Raw:   map[string]any{"public_id": id, "name": t.name, "type": t.typ, "status": t.status},
			URL:   WebBase(d.site) + "/synthetics/details/" + id,
		})
	}
	return rows
}
