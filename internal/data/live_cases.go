package data

import (
	"context"
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
			},
			Raw: c,
			URL: l.web + "/cases/" + attrs.GetKey(),
		})
	}
	return rows, nil
}
