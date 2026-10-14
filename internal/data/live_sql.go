package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// sqlRowLimit bounds a DDSQL result so a careless SELECT cannot flood the
// pane; the row count is shown so truncation is never silent.
const sqlRowLimit = 200

// sqlPollEvery / sqlPollMax bound the fetch loop for queries the service
// answers asynchronously (state "running" with a query id to fetch later).
const (
	sqlPollEvery = 700 * time.Millisecond
	sqlPollMax   = 20
)

// SQLQuery runs a DDSQL query over the window. The service may answer
// synchronously or hand back a running query id; both paths end in the same
// columnar result.
func (l *Live) SQLQuery(ctx context.Context, query string, window time.Duration) (*SQLResult, error) {
	if window <= 0 {
		window = time.Hour
	}
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	api := datadogV2.NewDDSQLApi(l.client)

	to := time.Now().Unix()
	from := to - int64(window/time.Second)
	attrs := datadogV2.NewDdsqlTabularQueryRequestAttributes(query, *datadogV2.NewDdsqlTabularQueryTimeWindow(from, to))
	attrs.RowLimit = datadog.PtrInt64(sqlRowLimit)
	body := datadogV2.NewDdsqlTabularQueryRequest(
		*datadogV2.NewDdsqlTabularQueryRequestData(*attrs, datadogV2.DDSQLTABULARQUERYREQUESTTYPE_DDSQL_QUERY_REQUEST))
	resp, httpresp, err := api.ExecuteDdsqlTabularQuery(ctx, *body)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("ddsql", err)
	}
	data := resp.GetData()
	rattrs := data.GetAttributes()
	for i := 0; rattrs.GetState() == datadogV2.DDSQLTABULARQUERYSTATE_RUNNING && i < sqlPollMax; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sqlPollEvery):
		}
		fb := datadogV2.NewDdsqlTabularQueryFetchRequest(*datadogV2.NewDdsqlTabularQueryFetchRequestData(
			*datadogV2.NewDdsqlTabularQueryFetchRequestAttributes(rattrs.GetQueryId()),
			datadogV2.DDSQLTABULARQUERYFETCHREQUESTTYPE_DDSQL_QUERY_FETCH_REQUEST))
		fresp, fhttp, ferr := api.FetchDdsqlTabularQuery(ctx, *fb)
		l.track(fhttp)
		if ferr != nil {
			return nil, apiErr("ddsql fetch", ferr)
		}
		data = fresp.GetData()
		rattrs = data.GetAttributes()
	}
	if rattrs.GetState() == datadogV2.DDSQLTABULARQUERYSTATE_RUNNING {
		return nil, fmt.Errorf("ddsql: query still running after %s — narrow the window or the query", time.Duration(sqlPollMax)*sqlPollEvery)
	}
	return sqlResultFromColumns(query, rattrs.GetColumns(), rattrs.GetWarnings()), nil
}

// sqlResultFromColumns pivots DDSQL's column-major answer into rows.
func sqlResultFromColumns(query string, cols []datadogV2.DdsqlTabularQueryColumn, warnings []string) *SQLResult {
	res := &SQLResult{Query: query, Warnings: warnings}
	n := 0
	for _, c := range cols {
		res.Columns = append(res.Columns, c.GetName())
		if len(c.GetValues()) > n {
			n = len(c.GetValues())
		}
	}
	res.Rows = make([][]string, n)
	for i := range res.Rows {
		res.Rows[i] = make([]string, len(cols))
		for j, c := range cols {
			vals := c.GetValues()
			if i < len(vals) {
				res.Rows[i][j] = sqlCell(vals[i])
			}
		}
	}
	res.Truncated = n >= sqlRowLimit
	return res
}

// sqlCell renders one DDSQL value for the table: scalars as-is, nested
// values compactly, nulls as a dash.
func sqlCell(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		return t
	case float64:
		return FormatValue(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
