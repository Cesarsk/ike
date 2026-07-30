package data

import (
	"context"
	"sort"
	"strings"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

func (l *Live) cases(ctx context.Context, query string) ([]Row, error) {
	api := datadogV2.NewCaseManagementApi(l.client)
	opts := datadogV2.NewSearchCasesOptionalParameters().
		WithPageSize(100).
		WithSortField(datadogV2.CASESORTABLEFIELD_CREATED_AT).
		WithSortAsc(false)
	q := strings.TrimSpace(query)
	if q != "" && q != "*" {
		opts = opts.WithFilter(q)
	}
	resp, httpresp, err := api.SearchCases(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("cases", err)
	}
	list := resp.GetData()
	rows := make([]Row, 0, len(list))
	for _, c := range list {
		attrs := c.GetAttributes()
		status := attrs.GetStatusName()
		if status == "" {
			status = string(attrs.GetStatus())
		}
		created := ""
		if t := attrs.GetCreatedAt(); !t.IsZero() {
			created = t.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, Row{
			ID: c.GetId(),
			Cells: []string{
				attrs.GetKey(),
				status,
				string(attrs.GetPriority()),
				attrs.GetTitle(),
				created,
				caseTags(attrs.GetAttributes()),
			},
			Raw: c,
			URL: l.web + "/cases/" + attrs.GetKey(),
		})
	}
	return rows, nil
}

// caseTags flattens a case's free-form attribute map (Case Management's
// labels) into "k:v" tags, so the shared filter syntax applies.
func caseTags(attrs map[string][]string) string {
	out := make([]string, 0, len(attrs))
	for k, vals := range attrs {
		if len(vals) == 0 {
			out = append(out, k)
			continue
		}
		for _, v := range vals {
			out = append(out, k+":"+v)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}
