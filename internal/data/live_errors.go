package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// errorIssues lists Error Tracking issues over the last 24h, highest total
// count first (the API orders). The query is an ET search ('/'). The response
// is JSON:API: data carries per-issue counts, included[] the issue objects.
func (l *Live) errorIssues(ctx context.Context, query string) ([]Row, error) {
	if query == "*" {
		query = ""
	}
	now := time.Now()
	attrs := datadogV2.NewIssuesSearchRequestDataAttributes(
		now.Add(-24*time.Hour).UnixMilli(), query, now.UnixMilli())
	orderBy := datadogV2.ISSUESSEARCHREQUESTDATAATTRIBUTESORDERBY_TOTAL_COUNT
	attrs.OrderBy = &orderBy
	body := datadogV2.NewIssuesSearchRequest(*datadogV2.NewIssuesSearchRequestData(
		*attrs, datadogV2.ISSUESSEARCHREQUESTDATATYPE_SEARCH_REQUEST))
	resp, httpresp, err := datadogV2.NewErrorTrackingApi(l.client).SearchIssues(ctx, *body,
		*datadogV2.NewSearchIssuesOptionalParameters().WithInclude(
			[]datadogV2.SearchIssuesIncludeQueryParameterItem{datadogV2.SEARCHISSUESINCLUDEQUERYPARAMETERITEM_ISSUE}))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("error issues", err)
	}

	// Resolve included[] issues by id, then walk the results in API order.
	issues := map[string]datadogV2.Issue{}
	for _, inc := range resp.GetIncluded() {
		if inc.Issue != nil {
			issues[inc.Issue.GetId()] = *inc.Issue
		}
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, res := range data {
		rels := res.GetRelationships()
		rel := rels.GetIssue()
		relData := rel.GetData()
		issue, ok := issues[relData.GetId()]
		if !ok {
			continue
		}
		a := issue.GetAttributes()
		typ := a.GetErrorType()
		if a.GetIsCrash() {
			typ += " (crash)"
		}
		resAttrs := res.GetAttributes()
		rows = append(rows, Row{
			ID: issue.GetId(),
			Cells: []string{
				errorAge(a.GetLastSeen()),
				strings.ToLower(string(a.GetState())),
				fmt.Sprintf("%d", resAttrs.GetTotalCount()),
				typ,
				firstLine(a.GetErrorMessage()),
				a.GetService(),
			},
			Raw:      issue,
			URL:      l.web + "/error-tracking?issueId=" + issue.GetId(),
			LogQuery: errorLogQuery(a.GetService()), // l → the service's error logs
		})
	}
	return rows, nil
}

// SetIssueState triages an Error Tracking issue.
func (l *Live) SetIssueState(ctx context.Context, id, state string) error {
	st, err := datadogV2.NewIssueStateFromValue(strings.ToUpper(state))
	if err != nil {
		return fmt.Errorf("issue state: %w", err)
	}
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := datadogV2.NewIssueUpdateStateRequest(*datadogV2.NewIssueUpdateStateRequestData(
		*datadogV2.NewIssueUpdateStateRequestDataAttributes(*st), id,
		datadogV2.ISSUEUPDATESTATEREQUESTDATATYPE_ERROR_TRACKING_ISSUE))
	_, httpresp, err := datadogV2.NewErrorTrackingApi(l.client).UpdateIssueState(ctx, id, *body)
	l.track(httpresp)
	return apiErr("issue state", err)
}

// errorLogQuery is the logs drill for an issue: the owning service's error
// logs (issues don't carry a log query of their own).
func errorLogQuery(service string) string {
	if service == "" {
		return ""
	}
	return "service:" + service + " status:error"
}

// errorAge turns a millisecond epoch into a short age.
func errorAge(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
