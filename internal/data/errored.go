package data

import "context"

// Errored is a placeholder Provider for a context whose credentials could
// not be resolved at startup. Instead of exiting, the app opens on the :ctx
// view with the resolution error, so a first-time user can add a context
// (and paste keys) entirely from inside the TUI.
type Errored struct {
	site string
	err  error
}

func NewErrored(site string, err error) *Errored { return &Errored{site: site, err: err} }

func (e *Errored) Fetch(context.Context, string, string, string) ([]Row, error) {
	return nil, e.err
}
func (e *Errored) FetchDetail(context.Context, string, string) (any, error) {
	return nil, e.err
}
func (e *Errored) Dashboard(context.Context, string) (*DashboardView, error) {
	return nil, e.err
}
func (e *Errored) SetIncidentState(context.Context, string, string) error { return e.err }
func (e *Errored) SetMonitorMute(context.Context, string, bool) error     { return e.err }
func (e *Errored) Budget() []string                                       { return nil }
func (e *Errored) Mode() string                                           { return "live" }
func (e *Errored) Site() string                                           { return e.site }
