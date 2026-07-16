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
}

type entry struct {
	rows []Row
	at   time.Time
}

func NewCached(p Provider) *Cached {
	return &Cached{p: p, entries: map[string]*entry{}}
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

// SetIncidentState writes through and drops the incidents cache so the next
// fetch reflects the change.
func (c *Cached) SetIncidentState(ctx context.Context, id, state string) error {
	if err := c.p.SetIncidentState(ctx, id, state); err != nil {
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
