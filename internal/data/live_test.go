package data

import (
	"testing"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

func TestMonitorLogQuery(t *testing.T) {
	logMon := datadogV1.Monitor{}
	logMon.SetType(datadogV1.MONITORTYPE_LOG_ALERT)
	logMon.SetQuery(`logs("service:kong-proxy status:error").index("*").rollup("count").last("5m") > 100`)
	logMon.SetTags([]string{"team:sre"})
	if got := monitorLogQuery(logMon); got != "service:kong-proxy status:error" {
		t.Errorf("log alert: %q", got)
	}

	metricMon := datadogV1.Monitor{}
	metricMon.SetType(datadogV1.MONITORTYPE_METRIC_ALERT)
	metricMon.SetQuery("avg(last_5m):avg:system.cpu.user{service:payments-api} > 90")
	metricMon.SetTags([]string{"team:payments", "service:payments-api", "env:prod"})
	if got := monitorLogQuery(metricMon); got != "service:payments-api env:prod" {
		t.Errorf("metric alert tags: %q", got)
	}

	bare := datadogV1.Monitor{}
	bare.SetType(datadogV1.MONITORTYPE_METRIC_ALERT)
	bare.SetTags([]string{"team:sre"})
	if got := monitorLogQuery(bare); got != "" {
		t.Errorf("no derivable query should be empty, got %q", got)
	}
}
