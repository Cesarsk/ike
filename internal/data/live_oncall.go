package data

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
	"github.com/google/uuid"
)

// oncallTeams lists the org's teams, the discoverable entry point to On-Call
// (the API has no "list schedules" endpoint). Who is on call is fetched per
// team on drill-in, so this stays one bounded call.
func (l *Live) oncallTeams(ctx context.Context) ([]Row, error) {
	data, err := l.listTeams(ctx)
	if err != nil {
		return nil, err
	}
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

	// Best-effort: enrich each rung with its escalate-after delay from the
	// team's escalation policy. The policy is reached through the routing
	// rules; any failure just leaves the delays at zero, so the panel never
	// regresses to nothing.
	if delays := l.teamStepDelays(ctx, teamID); len(delays) > 0 {
		for i := range det.Escalation {
			if i < len(delays) {
				det.Escalation[i].DelayMin = delays[i]
			}
		}
	}
	return det, nil
}

// teamStepDelays returns the escalate-after delay (minutes) per escalation
// step for a team, in step order. Best-effort: team → routing rules → first
// escalation policy → step delays. Returns nil on any miss.
func (l *Live) teamStepDelays(ctx context.Context, teamID string) []int {
	api := datadogV2.NewOnCallApi(l.client)
	rules, _, err := api.GetOnCallTeamRoutingRules(ctx, teamID)
	if err != nil {
		return nil
	}
	policyID := ""
	for _, inc := range rules.GetIncluded() {
		if inc.RoutingRule == nil {
			continue
		}
		attrs := inc.RoutingRule.GetAttributes()
		for _, act := range attrs.GetActions() {
			if act.RoutingRuleEscalationPolicyAction != nil {
				if id := act.RoutingRuleEscalationPolicyAction.GetPolicyId(); id != "" {
					policyID = id
					break
				}
			}
		}
		if policyID != "" {
			break
		}
	}
	if policyID == "" {
		return nil
	}

	policy, _, err := api.GetOnCallEscalationPolicy(ctx, policyID,
		*datadogV2.NewGetOnCallEscalationPolicyOptionalParameters().WithInclude("steps"))
	if err != nil {
		return nil
	}
	stepMin := map[string]int{}
	for _, inc := range policy.GetIncluded() {
		if inc.EscalationPolicyStep != nil {
			sa := inc.EscalationPolicyStep.GetAttributes()
			stepMin[inc.EscalationPolicyStep.GetId()] = int(sa.GetEscalateAfterSeconds()) / 60
		}
	}
	data := policy.GetData()
	rel := data.GetRelationships()
	steps := rel.GetSteps()
	var out []int
	for _, ref := range steps.GetData() {
		out = append(out, stepMin[ref.GetId()])
	}
	return out
}

// PageTeam raises an On-Call page against a team. urgency is "high" or "low"
// (anything else defaults to low). Returns the new page's id.
func (l *Live) PageTeam(ctx context.Context, teamID, title, urgency, description string) (string, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	u := datadogV2.PAGEURGENCY_LOW
	if urgency == "high" {
		u = datadogV2.PAGEURGENCY_HIGH
	}
	body := datadogV2.CreatePageRequest{
		Data: &datadogV2.CreatePageRequestData{
			Type: datadogV2.CREATEPAGEREQUESTDATATYPE_PAGES,
			Attributes: &datadogV2.CreatePageRequestDataAttributes{
				Title:       title,
				Description: datadog.PtrString(description),
				Urgency:     u,
				Target: datadogV2.CreatePageRequestDataAttributesTarget{
					Identifier: datadog.PtrString(teamID),
					Type:       datadogV2.ONCALLPAGETARGETTYPE_TEAM_ID.Ptr(),
				},
			},
		},
	}
	resp, httpresp, err := datadogV2.NewOnCallPagingApi(l.client).CreateOnCallPage(ctx, body)
	l.track(httpresp)
	if err != nil {
		return "", apiErr("page", err)
	}
	pageData := resp.GetData()
	return pageData.GetId(), nil
}

// AckPage acknowledges a page by id.
func (l *Live) AckPage(ctx context.Context, pageID string) error {
	return l.pageLifecycle(ctx, pageID, "acknowledge")
}

// EscalatePage escalates a page to the next level by id.
func (l *Live) EscalatePage(ctx context.Context, pageID string) error {
	return l.pageLifecycle(ctx, pageID, "escalate")
}

// ResolvePage resolves a page by id.
func (l *Live) ResolvePage(ctx context.Context, pageID string) error {
	return l.pageLifecycle(ctx, pageID, "resolve")
}

// pageLifecycle runs an acknowledge/escalate/resolve action on a page.
func (l *Live) pageLifecycle(ctx context.Context, pageID, action string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, err := uuid.Parse(pageID)
	if err != nil {
		return fmt.Errorf("page %s: invalid page id %q", action, pageID)
	}
	api := datadogV2.NewOnCallPagingApi(l.client)
	var httpresp *http.Response
	switch action {
	case "acknowledge":
		httpresp, err = api.AcknowledgeOnCallPage(ctx, id)
	case "escalate":
		httpresp, err = api.EscalateOnCallPage(ctx, id)
	case "resolve":
		httpresp, err = api.ResolveOnCallPage(ctx, id)
	}
	l.track(httpresp)
	if err != nil {
		return apiErr("page "+action, err)
	}
	return nil
}
