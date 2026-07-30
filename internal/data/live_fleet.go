package data

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// fleet lists the Datadog agents across the estate (Fleet Automation) —
// the hygiene view: which hosts run which agent version.
func (l *Live) fleet(ctx context.Context, query string) ([]Row, error) {
	opts := datadogV2.NewListFleetAgentsOptionalParameters().WithPageSize(200)
	q := strings.TrimSpace(query)
	if q != "" && q != "*" {
		opts = opts.WithTags(q)
	}
	resp, httpresp, err := datadogV2.NewFleetAutomationApi(l.client).ListFleetAgents(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("fleet agents", err)
	}
	respData := resp.GetData()
	respAttrs := respData.GetAttributes()
	agents := respAttrs.GetAgents()
	rows := make([]Row, 0, len(agents))
	for _, a := range agents {
		restarted := ""
		if ts := a.GetLastRestartAt(); ts > 0 {
			restarted = HumanWindow(time.Since(msToTime(float64(ts))).Truncate(time.Minute))
		}
		rows = append(rows, Row{
			ID: a.GetHostname(),
			Cells: []string{
				a.GetHostname(),
				a.GetAgentVersion(),
				a.GetClusterName(),
				a.GetOs(),
				strings.Join(a.GetEnvs(), ","),
				restarted,
				fleetTags(a.GetTags()),
			},
			Raw: a,
			URL: l.web + "/fleet",
		})
	}
	// Oldest agent versions first — the rows that need attention.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cells[1] != rows[j].Cells[1] {
			return rows[i].Cells[1] < rows[j].Cells[1]
		}
		return rows[i].Cells[0] < rows[j].Cells[0]
	})
	return rows, nil
}

// fleetTags flattens the fleet agent's key/value tag pairs into the "k:v"
// form every other view uses, so one filter syntax works everywhere.
func fleetTags(items []datadogV2.FleetAgentAttributesTagsItems) string {
	out := make([]string, 0, len(items))
	for _, t := range items {
		if k := t.GetKey(); k != "" {
			out = append(out, k+":"+t.GetValue())
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}
