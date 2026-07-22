package data

import "context"

// Errored is a placeholder Provider for a context whose credentials could
// not be resolved at startup. Instead of exiting, the app opens on the :ctx
// view with the resolution error, so a first-time user can add a context
// (and paste keys) entirely from inside the TUI.
// Errored satisfies Provider.
var _ Provider = (*Errored)(nil)

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
func (e *Errored) Trace(context.Context, string) (*TraceView, error) {
	return nil, e.err
}
func (e *Errored) LogContext(context.Context, Row, int) (*LogContextView, error) {
	return nil, e.err
}
func (e *Errored) Cost(context.Context, CostOptions) (*CostView, error) {
	return nil, e.err
}
func (e *Errored) TeamOnCall(context.Context, string) (*OnCallDetail, error) {
	return nil, e.err
}
func (e *Errored) TeamMembers(context.Context, string) ([]TeamMember, error) {
	return nil, e.err
}
func (e *Errored) Notebook(context.Context, string) (*NotebookView, error) {
	return nil, e.err
}
func (e *Errored) PageTeam(context.Context, string, string, string, string) (string, error) {
	return "", e.err
}
func (e *Errored) AckPage(context.Context, string) error      { return e.err }
func (e *Errored) EscalatePage(context.Context, string) error { return e.err }
func (e *Errored) ResolvePage(context.Context, string) error  { return e.err }
func (e *Errored) MonitorMetric(context.Context, string) (*MetricSeries, error) {
	return nil, e.err
}
func (e *Errored) SetIncidentField(context.Context, string, string, string) error { return e.err }
func (e *Errored) SetMonitorMute(context.Context, string, bool) error             { return e.err }
func (e *Errored) CancelDowntime(context.Context, string) error                   { return e.err }
func (e *Errored) CurrentUser(context.Context) (User, error)                      { return User{}, e.err }
func (e *Errored) SetIncidentCommander(context.Context, string, string) error     { return e.err }
func (e *Errored) AddIncidentTodo(context.Context, string, string, string) error  { return e.err }
func (e *Errored) ListUsers(context.Context, string) ([]User, error)              { return nil, e.err }
func (e *Errored) IncidentTodos(context.Context, string) ([]Todo, error)          { return nil, e.err }
func (e *Errored) SetIncidentTodoCompleted(context.Context, string, Todo, bool) error {
	return e.err
}
func (e *Errored) DeleteIncidentTodo(context.Context, string, string) error  { return e.err }
func (e *Errored) IncidentImpacts(context.Context, string) ([]string, error) { return nil, e.err }
func (e *Errored) Budget() []string                                          { return nil }
func (e *Errored) Mode() string                                              { return "live" }
func (e *Errored) Site() string                                              { return e.site }
