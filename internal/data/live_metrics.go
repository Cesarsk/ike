package data

import (
	"context"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

// MetricQuery runs a free-form v1 metric query over the window: the same
// engine the classic dashboard widgets use, so grouped queries (by {host})
// come back as multiple series and chart as the per-bucket max.
func (l *Live) MetricQuery(ctx context.Context, query string, window time.Duration) (*MetricExplorer, error) {
	if window <= 0 {
		window = time.Hour
	}
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	to := time.Now().Unix()
	from := to - int64(window/time.Second)
	mq, resp, err := datadogV1.NewMetricsApi(l.client).QueryMetrics(ctx, from, to, query)
	l.track(resp)
	if err != nil {
		return nil, apiErr("metric query", err)
	}
	out := &MetricExplorer{Query: query}
	series := mq.GetSeries()
	vals := make([][]*float64, len(series))
	for si, s := range series {
		for _, pair := range s.GetPointlist() {
			if len(pair) == 2 {
				vals[si] = append(vals[si], pair[1])
			}
		}
	}
	out.Spark = envelopeMax(vals)
	last, ok := LastValid(out.Spark)
	if !ok {
		out.Note = "no data in last " + HumanWindow(window)
		return out, nil
	}
	out.Last = last
	out.Series = len(series)
	out.Items = seriesLastValues(mq, 10)
	return out, nil
}
