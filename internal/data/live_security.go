package data

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// securitySignals lists Cloud SIEM / CSM security signals, newest first. The
// query is a server-side signals search ('/'); the window follows the digit
// keys like logs, defaulting to the last 24h. Read-only.
func (l *Live) securitySignals(ctx context.Context, query, timeRange string) ([]Row, error) {
	from := time.Now().Add(-24 * time.Hour)
	if s, ok := rangeSeconds(timeRange); ok {
		from = time.Now().Add(-time.Duration(s) * time.Second)
	}
	if query == "*" {
		query = ""
	}
	p := datadogV2.NewListSecurityMonitoringSignalsOptionalParameters().
		WithFilterFrom(from).WithFilterTo(time.Now()).
		WithSort(datadogV2.SECURITYMONITORINGSIGNALSSORT_TIMESTAMP_DESCENDING).
		WithPageLimit(100)
	if query != "" {
		p = p.WithFilterQuery(query)
	}
	resp, httpresp, err := datadogV2.NewSecurityMonitoringApi(l.client).ListSecurityMonitoringSignals(ctx, *p)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("security signals", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, s := range data {
		a := s.GetAttributes()
		tags := a.GetTags()
		rows = append(rows, Row{
			ID: s.GetId(),
			Cells: []string{
				a.GetTimestamp().Local().Format("2006-01-02 15:04"),
				signalSeverity(tags),
				firstLine(a.GetMessage()),
				strings.Join(tags, " "),
			},
			Raw: s,
			URL: l.web + "/security?query=" + url.QueryEscape(query),
		})
	}
	return rows, nil
}

// signalSeverity pulls the severity from a signal's tags (best-effort — the
// typed attributes don't expose it directly; it rides in a "severity:" tag).
func signalSeverity(tags []string) string {
	for _, t := range tags {
		if v := strings.TrimPrefix(t, "severity:"); v != t {
			return v
		}
	}
	return ""
}
