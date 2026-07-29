package data

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
)

// rawGet calls a Datadog endpoint the typed client doesn't expose, with the
// same per-org auth the typed operations use (key pair headers or bearer via
// the request context). Kept deliberately small: GET + JSON only.
func (l *Live) rawGet(ctx context.Context, path string, query url.Values, out any) error {
	headers := map[string]string{"Accept": "application/json"}
	datadog.SetAuthKeys(ctx, &headers,
		[2]string{"apiKeyAuth", "DD-API-KEY"},
		[2]string{"appKeyAuth", "DD-APPLICATION-KEY"})
	req, err := l.client.PrepareRequest(ctx, "https://api."+l.site+path, http.MethodGet, nil, headers, query, nil, nil)
	if err != nil {
		return err
	}
	resp, err := l.client.CallAPI(req)
	if err != nil {
		return err
	}
	l.track(resp)
	body, err := datadog.ReadBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return json.Unmarshal(body, out)
}

// deps backs the :deps view — the service dependency map. The endpoint
// (/api/v1/service_dependencies) is not in the typed client but is the same
// one the web service map and pup's `apm dependencies` use; treat it as
// unstable and degrade gracefully.
func (l *Live) deps(ctx context.Context, query string) ([]Row, error) {
	env := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "env:"))
	if env == "" || env == "*" {
		env = "prod"
	}
	to := time.Now().Unix()
	from := to - 3600
	q := url.Values{}
	q.Set("env", env)
	q.Set("start", strconv.FormatInt(from, 10))
	q.Set("end", strconv.FormatInt(to, 10))
	var graph map[string]struct {
		Calls []string `json:"calls"`
	}
	if err := l.rawGet(ctx, "/api/v1/service_dependencies", q, &graph); err != nil {
		return nil, apiErr("service dependencies", err)
	}
	calledBy := map[string][]string{}
	for svc, d := range graph {
		for _, callee := range d.Calls {
			calledBy[callee] = append(calledBy[callee], svc)
		}
	}
	rows := make([]Row, 0, len(graph))
	for svc, d := range graph {
		up := calledBy[svc]
		sort.Strings(up)
		down := append([]string(nil), d.Calls...)
		sort.Strings(down)
		rows = append(rows, Row{
			ID: svc,
			Cells: []string{
				svc,
				strconv.Itoa(len(down)),
				strconv.Itoa(len(up)),
				strings.Join(down, " "),
			},
			Raw: map[string]any{"env": env, "calls": down, "called_by": up},
			URL: l.web + "/apm/map?env=" + url.QueryEscape(env),
		})
	}
	// Most connected first: the services that matter in the graph.
	sort.SliceStable(rows, func(i, j int) bool {
		ci := len(graph[rows[i].ID].Calls) + len(calledBy[rows[i].ID])
		cj := len(graph[rows[j].ID].Calls) + len(calledBy[rows[j].ID])
		if ci != cj {
			return ci > cj
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}
