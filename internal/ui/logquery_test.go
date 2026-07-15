package ui

import (
	"reflect"
	"testing"

	"github.com/Cesarsk/ike/internal/data"
)

func TestLogQueryCompletions(t *testing.T) {
	// Loaded log rows: columns are TIME, STATUS, SERVICE, HOST, MESSAGE.
	a := &App{rows: []data.Row{
		{Cells: []string{"12:00:00", "error", "kong-proxy", "ip-10-1-2-11", "boom"}},
		{Cells: []string{"12:00:01", "info", "payments-api", "ip-10-1-4-23", "ok"}},
		{Cells: []string{"12:00:02", "error", "kong-proxy", "ip-10-1-2-11", "boom2"}},
	}}

	tests := []struct {
		name  string
		field string
		want  []string
	}{
		{"facet key prefix", "serv", []string{"service:"}},
		{"value from loaded rows", "service:ko", []string{"service:kong-proxy"}},
		{"status values distinct + sorted", "status:", []string{"status:error", "status:info"}},
		{"preserves earlier tokens", "status:error serv", []string{"status:error service:"}},
		{"value completion keeps prefix", "status:error service:pay", []string{"status:error service:payments-api"}},
		{"unknown facet has no values", "env:", nil},
		{"empty token yields nothing", "status:error ", nil},
		{"complete token offers no dropdown (Enter must submit)", "status:error", nil},
		{"complete value mid-query offers nothing", "service:kong-proxy", nil},
	}
	for _, tc := range tests {
		got := a.logQueryCompletions(tc.field)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: logQueryCompletions(%q) = %v, want %v", tc.name, tc.field, got, tc.want)
		}
	}
}
