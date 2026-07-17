package data

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
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

// TestCommanderUpdateBodyJSON asserts the wire shape of the commander-assign
// request. The nested nullable-relationship construction can't be runtime-
// tested from the sandbox, so this locks the JSON Datadog expects:
// relationships.commander_user.data = {id, type: "users"}.
func TestCommanderUpdateBodyJSON(t *testing.T) {
	b, err := json.Marshal(commanderUpdateBody("inc-abc", "user-42"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"commander_user"`, `"data"`, `"id":"user-42"`, `"type":"users"`} {
		if !strings.Contains(s, want) {
			t.Errorf("commander body missing %q\n got: %s", want, s)
		}
	}
}

// TestTodoCompletedPatchBody asserts the wire shape of the to-do completion
// PATCH: content and assignees are re-sent so they aren't blanked, and
// completed is a timestamp when done / explicit null when reopened.
func TestTodoCompletedPatchBody(t *testing.T) {
	todo := Todo{ID: "td-1", Content: "Page the DBA", Assignees: []string{"alice"}}

	done, err := json.Marshal(todoCompletedPatchBody(todo, true, "2026-07-17T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"type":"incident_todos"`, `"content":"Page the DBA"`, `"assignees"`, `"alice"`,
		`"completed":"2026-07-17T00:00:00Z"`,
	} {
		if !strings.Contains(string(done), want) {
			t.Errorf("done body missing %q\n got: %s", want, done)
		}
	}

	reopened, err := json.Marshal(todoCompletedPatchBody(todo, false, "2026-07-17T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reopened), `"completed":null`) {
		t.Errorf("reopened body should null the completion\n got: %s", reopened)
	}
	// Content/assignees must survive the reopen too (no blanking).
	if !strings.Contains(string(reopened), `"content":"Page the DBA"`) {
		t.Errorf("reopened body dropped content\n got: %s", reopened)
	}
}

// TestIncidentFieldUnion covers both arms of the IncidentFieldAttributes union
// plus a missing field — the hardening that keeps a real org's custom fields
// from blanking the SEV/STATE columns.
func TestIncidentFieldUnion(t *testing.T) {
	sv := datadogV2.NewIncidentFieldAttributesSingleValue()
	sv.SetValue("SEV-1")
	mv := datadogV2.NewIncidentFieldAttributesMultipleValue()
	mv.SetValue([]string{"payments", "trading"})

	fields := map[string]datadogV2.IncidentFieldAttributes{
		"severity": datadogV2.IncidentFieldAttributesSingleValueAsIncidentFieldAttributes(sv),
		"teams":    datadogV2.IncidentFieldAttributesMultipleValueAsIncidentFieldAttributes(mv),
	}
	if got := incidentField(fields, "severity"); got != "SEV-1" {
		t.Errorf("single-value = %q, want SEV-1", got)
	}
	if got := incidentField(fields, "teams"); got != "payments, trading" {
		t.Errorf("multi-value = %q, want joined", got)
	}
	if got := incidentField(fields, "nope"); got != "" {
		t.Errorf("missing field = %q, want empty", got)
	}
}
