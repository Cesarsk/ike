package data

import (
	"context"
	"net/url"
	"strings"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

func (l *Live) audit(ctx context.Context, query, timeRange string) ([]Row, error) {
	if strings.TrimSpace(query) == "" {
		query = "*"
	}
	if timeRange == "" {
		timeRange = "now-1d" // audit events are sparse; 15m is usually empty
	}
	api := datadogV2.NewAuditApi(l.client)
	body := datadogV2.AuditLogsSearchEventsRequest{
		Filter: &datadogV2.AuditLogsQueryFilter{
			Query: datadog.PtrString(query),
			From:  datadog.PtrString(timeRange),
			To:    datadog.PtrString("now"),
		},
		Sort: datadogV2.AUDITLOGSSORT_TIMESTAMP_DESCENDING.Ptr(),
		Page: &datadogV2.AuditLogsQueryPageOptions{Limit: datadog.PtrInt32(100)},
	}
	resp, httpresp, err := api.SearchAuditLogs(ctx,
		*datadogV2.NewSearchAuditLogsOptionalParameters().WithBody(body))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("audit", err)
	}
	events := resp.GetData()
	rows := make([]Row, 0, len(events))
	for _, ev := range events {
		attrs := ev.GetAttributes()
		free := attrs.GetAttributes()
		user := rumStr(rumMap(free, "usr"), "email")
		if user == "" {
			user = rumStr(rumMap(free, "usr"), "name")
		}
		action := rumStr(rumMap(free, "evt"), "name")
		msg := attrs.GetMessage()
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		rows = append(rows, Row{
			ID: ev.GetId(),
			Cells: []string{
				attrs.GetTimestamp().Local().Format("01-02 15:04"),
				attrs.GetService(),
				action,
				user,
				msg,
			},
			Raw: ev,
			URL: l.web + "/audit-trail?query=" + url.QueryEscape(query),
		})
	}
	return rows, nil
}
