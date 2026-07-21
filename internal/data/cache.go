package data

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Cached wraps a Provider with a per-resource TTL cache. This is the core
// defence against Datadog's per-organization API rate limits: repeated
// navigation between views is served from cache, and only an explicit
// refresh (Ctrl-R) or TTL expiry hits the API.
type Cached struct {
	p       Provider
	mu      sync.Mutex
	entries map[string]*entry

	uMu    sync.Mutex
	ucache map[string]*ucacheEntry // ListUsers results, keyed by query
}

type entry struct {
	rows []Row
	at   time.Time
}

type ucacheEntry struct {
	users []User
	at    time.Time
}

// userCacheTTL bounds how long a user-search result is reused. Users change
// rarely, so re-searching the same query within the window is served locally —
// the picker's debounce plus this cache keep the users endpoint cheap.
const userCacheTTL = 2 * time.Minute

func NewCached(p Provider) *Cached {
	return &Cached{p: p, entries: map[string]*entry{}, ucache: map[string]*ucacheEntry{}}
}

func (c *Cached) Mode() string     { return c.p.Mode() }
func (c *Cached) Site() string     { return c.p.Site() }
func (c *Cached) Budget() []string { return c.p.Budget() }

// FetchDetail is an explicit on-demand action (enter on a row), so it is
// deliberately not cached: one keypress, one call.
func (c *Cached) FetchDetail(ctx context.Context, key, id string) (any, error) {
	return c.p.FetchDetail(ctx, key, id)
}

// Dashboard is an explicit render/refresh action, deliberately uncached —
// each open or ctrl-r spends metric-query budget knowingly.
func (c *Cached) Dashboard(ctx context.Context, id string) (*DashboardView, error) {
	return c.p.Dashboard(ctx, id)
}

// Trace is an explicit drill-down, deliberately uncached.
func (c *Cached) Trace(ctx context.Context, traceID string) (*TraceView, error) {
	return c.p.Trace(ctx, traceID)
}

// LogContext is an on-demand, single-call fetch, uncached.
func (c *Cached) LogContext(ctx context.Context, anchor Row, windowSecs int) (*LogContextView, error) {
	return c.p.LogContext(ctx, anchor, windowSecs)
}

// Cost passes through to the provider; the UI panel fetches it on demand and
// the figures move at most daily, so caching lives in the UI's refresh cadence.
func (c *Cached) Cost(ctx context.Context) (*CostView, error) {
	return c.p.Cost(ctx)
}

// MonitorMetric is an on-demand detail fetch, uncached.
func (c *Cached) MonitorMetric(ctx context.Context, id string) (*MetricSeries, error) {
	return c.p.MonitorMetric(ctx, id)
}

// SetIncidentField writes through and drops the incidents cache so the next
// fetch reflects the change.
func (c *Cached) SetIncidentField(ctx context.Context, id, field, value string) error {
	if err := c.p.SetIncidentField(ctx, id, field, value); err != nil {
		return err
	}
	c.dropResource("incidents")
	return nil
}

// SetMonitorMute writes through and drops the monitors cache so the next
// fetch reflects the new mute state.
func (c *Cached) SetMonitorMute(ctx context.Context, id string, mute bool) error {
	if err := c.p.SetMonitorMute(ctx, id, mute); err != nil {
		return err
	}
	c.dropResource("monitors")
	return nil
}

// CancelDowntime writes through and drops the downtimes cache so the next
// fetch reflects the cancellation.
func (c *Cached) CancelDowntime(ctx context.Context, id string) error {
	if err := c.p.CancelDowntime(ctx, id); err != nil {
		return err
	}
	c.dropResource("downtimes")
	return nil
}

// CurrentUser is a passthrough (uncached — cheap, on-demand for a write).
func (c *Cached) CurrentUser(ctx context.Context) (User, error) {
	return c.p.CurrentUser(ctx)
}

// SetIncidentCommander writes through and drops the incidents cache so a
// re-fetch reflects the change.
func (c *Cached) SetIncidentCommander(ctx context.Context, incidentID, userID string) error {
	if err := c.p.SetIncidentCommander(ctx, incidentID, userID); err != nil {
		return err
	}
	c.dropResource("incidents")
	return nil
}

// AddIncidentTodo writes through. To-dos don't surface in the incidents table,
// so no cache eviction is needed.
func (c *Cached) AddIncidentTodo(ctx context.Context, incidentID, content, assigneeHandle string) error {
	return c.p.AddIncidentTodo(ctx, incidentID, content, assigneeHandle)
}

// ListUsers is cached per query for a short window (users change rarely), so
// re-opening the picker or re-typing a prior search doesn't re-hit the API.
func (c *Cached) ListUsers(ctx context.Context, query string) ([]User, error) {
	c.uMu.Lock()
	e, ok := c.ucache[query]
	c.uMu.Unlock()
	if ok && time.Since(e.at) < userCacheTTL {
		return e.users, nil
	}
	users, err := c.p.ListUsers(ctx, query)
	if err != nil {
		return nil, err
	}
	c.uMu.Lock()
	c.ucache[query] = &ucacheEntry{users: users, at: time.Now()}
	c.uMu.Unlock()
	return users, nil
}

// IncidentTodos is an on-demand panel fetch, deliberately uncached — one open,
// one call; writes re-fetch to reflect changes.
func (c *Cached) IncidentTodos(ctx context.Context, incidentID string) ([]Todo, error) {
	return c.p.IncidentTodos(ctx, incidentID)
}

// SetIncidentTodoCompleted / DeleteIncidentTodo write through. To-dos aren't in
// the incidents table cache, so nothing to evict; the panel re-fetches.
func (c *Cached) SetIncidentTodoCompleted(ctx context.Context, incidentID string, todo Todo, done bool) error {
	return c.p.SetIncidentTodoCompleted(ctx, incidentID, todo, done)
}

func (c *Cached) DeleteIncidentTodo(ctx context.Context, incidentID, todoID string) error {
	return c.p.DeleteIncidentTodo(ctx, incidentID, todoID)
}

// IncidentImpacts is an on-demand detail fetch, uncached.
func (c *Cached) IncidentImpacts(ctx context.Context, incidentID string) ([]string, error) {
	return c.p.IncidentImpacts(ctx, incidentID)
}

// dropResource evicts all cache entries for a resource key.
func (c *Cached) dropResource(key string) {
	c.mu.Lock()
	for k := range c.entries {
		if strings.HasPrefix(k, key+"|") {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// Fetch returns rows for a resource, from cache when fresh.
// It reports the fetch time and whether the result came from cache.
func (c *Cached) Fetch(ctx context.Context, res Resource, query, timeRange string, force bool) ([]Row, time.Time, bool, error) {
	key := res.Key + "|" + query + "|" + timeRange

	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()

	if ok && !force && time.Since(e.at) < res.TTL {
		return e.rows, e.at, true, nil
	}

	rows, err := c.p.Fetch(ctx, res.Key, query, timeRange)
	if err != nil {
		// Serve stale data alongside the error if we have any.
		if ok {
			return e.rows, e.at, true, err
		}
		return nil, time.Time{}, false, err
	}

	now := time.Now()
	c.mu.Lock()
	c.entries[key] = &entry{rows: rows, at: now}
	c.mu.Unlock()
	return rows, now, false, nil
}

// Age returns how old the cached entry for a resource/query is.
func (c *Cached) Age(res Resource, query string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[res.Key+"|"+query]; ok {
		return time.Since(e.at), true
	}
	return 0, false
}
