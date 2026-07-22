package data

import (
	"context"
	"strconv"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// oncallTeams lists the org's teams, the discoverable entry point to On-Call
// (the API has no "list schedules" endpoint). Who is on call is fetched per
// team on drill-in, so this stays one bounded call.
func (l *Live) oncallTeams(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV2.NewTeamsApi(l.client).ListTeams(ctx,
		*datadogV2.NewListTeamsOptionalParameters().WithPageSize(100).WithSort(datadogV2.LISTTEAMSSORT_NAME))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("teams", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, t := range data {
		a := t.GetAttributes()
		members := ""
		if n := a.GetUserCount(); n > 0 {
			members = strconv.Itoa(int(n))
		}
		rows = append(rows, Row{
			ID:    t.GetId(),
			Cells: []string{a.GetName(), a.GetHandle(), members},
			Raw:   t,
			URL:   l.web + "/on-call/teams/" + t.GetId(),
		})
	}
	return rows, nil
}

// TeamOnCall resolves who is on call now for a team plus its escalation
// ladder, from one GetTeamOnCallUsers call. The response is JSON:API: the
// team's responders and escalations are references into an included[] union
// of users and escalations, which we index and walk.
func (l *Live) TeamOnCall(ctx context.Context, teamID string) (*OnCallDetail, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewOnCallApi(l.client).GetTeamOnCallUsers(ctx, teamID,
		*datadogV2.NewGetTeamOnCallUsersOptionalParameters().WithInclude("responders,escalations.responders"))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("on-call", err)
	}

	users := map[string]OnCallResponder{}
	escalations := map[string]datadogV2.Escalation{}
	for _, inc := range resp.GetIncluded() {
		switch {
		case inc.User != nil:
			a := inc.User.GetAttributes()
			users[inc.User.GetId()] = OnCallResponder{
				Name: a.GetName(), Handle: a.GetHandle(), Email: a.GetEmail(),
			}
		case inc.Escalation != nil:
			escalations[inc.Escalation.GetId()] = *inc.Escalation
		}
	}

	det := &OnCallDetail{URL: l.web + "/on-call/teams/" + teamID}
	data := resp.GetData()
	rel := data.GetRelationships()

	responders := rel.GetResponders()
	for _, ref := range responders.GetData() {
		if u, ok := users[ref.GetId()]; ok {
			det.OnCall = append(det.OnCall, u)
		}
	}

	escRel := rel.GetEscalations()
	for i, ref := range escRel.GetData() {
		esc, ok := escalations[ref.GetId()]
		if !ok {
			continue
		}
		escRels := esc.GetRelationships()
		escResponders := escRels.GetResponders()
		level := OnCallLevel{Level: i + 1}
		for _, r := range escResponders.GetData() {
			if u, ok := users[r.GetId()]; ok {
				level.Responders = append(level.Responders, u)
			}
		}
		if len(level.Responders) > 0 {
			det.Escalation = append(det.Escalation, level)
		}
	}
	return det, nil
}
