package data

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// schedules lists every On-Call schedule with who is on call right now.
// ListOnCallSchedules is new in the API; before it, on-call was only
// reachable per team. The current responder is one GetScheduleOnCallUser per
// schedule (schedules are few; bounded fan-out), best-effort blank.
func (l *Live) schedules(ctx context.Context, query string) ([]Row, error) {
	api := datadogV2.NewOnCallApi(l.client)
	opts := datadogV2.NewListOnCallSchedulesOptionalParameters().WithPageSize(100).WithInclude("teams")
	q := strings.TrimSpace(query)
	if q != "" && q != "*" {
		opts = opts.WithFilterQuery(q)
	}
	resp, httpresp, err := api.ListOnCallSchedules(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("on-call schedules", err)
	}
	teams := map[string]string{}
	for _, t := range resp.GetIncluded() {
		ta := t.GetAttributes()
		name := ta.GetHandle()
		if name == "" {
			name = ta.GetName()
		}
		teams[t.GetId()] = name
	}
	items := resp.GetData()
	rows := make([]Row, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, s := range items {
		attrs := s.GetAttributes()
		var owners []string
		rels := s.GetRelationships()
		teamRel := rels.GetTeams()
		for _, ref := range teamRel.GetData() {
			if n, ok := teams[ref.GetId()]; ok && n != "" {
				owners = append(owners, n)
			}
		}
		rows[i] = Row{
			ID: s.GetId(),
			Cells: []string{
				attrs.GetName(),
				"", // ON-CALL, filled below
				strings.Join(owners, ","),
				attrs.GetTimeZone(),
				strings.Join(attrs.GetTags(), " "),
			},
			Raw: s,
			URL: l.web + "/on-call/schedules/" + s.GetId(),
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i].Cells[1] = l.scheduleOnCallNow(ctx, api, id)
		}(i, s.GetId())
	}
	wg.Wait()
	// Schedules nobody covers right now sort first — that is the finding.
	sort.SliceStable(rows, func(i, j int) bool {
		ei, ej := rows[i].Cells[1] == "", rows[j].Cells[1] == ""
		if ei != ej {
			return ei
		}
		return rows[i].Cells[0] < rows[j].Cells[0]
	})
	return rows, nil
}

// scheduleOnCallNow names the user currently on shift for a schedule
// ("" when nobody is, or on any error — this is enrichment, never a failure).
func (l *Live) scheduleOnCallNow(ctx context.Context, api *datadogV2.OnCallApi, scheduleID string) string {
	shift, httpresp, err := api.GetScheduleOnCallUser(ctx, scheduleID,
		*datadogV2.NewGetScheduleOnCallUserOptionalParameters().WithInclude("user"))
	l.track(httpresp)
	if err != nil {
		return ""
	}
	for _, inc := range shift.GetIncluded() {
		if inc.ScheduleUser == nil {
			continue
		}
		ua := inc.ScheduleUser.GetAttributes()
		if n := ua.GetName(); n != "" {
			return n
		}
		if e := ua.GetEmail(); e != "" {
			return e
		}
	}
	return ""
}
