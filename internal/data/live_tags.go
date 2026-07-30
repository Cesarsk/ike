package data

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

// MaxTagBackfill caps a single tag-backfill fan-out. Dashboard GETs share a
// tight per-minute limiter, so the UI states the cost and confirms first.
const MaxTagBackfill = 200

// tagBackfillConcurrency keeps the fan-out polite against the same limiter.
const tagBackfillConcurrency = 4

// ResourceTags fetches tags one object at a time for resources whose list
// endpoint omits them. Only dashboards need this today: DashboardSummary
// carries no tags at all — they exist solely on the full dashboard object.
func (l *Live) ResourceTags(ctx context.Context, key string, ids []string) (map[string]string, error) {
	if key != "dashboards" {
		return nil, fmt.Errorf("no tag backfill for %s (its list already carries tags)", key)
	}
	if len(ids) > MaxTagBackfill {
		ids = ids[:MaxTagBackfill]
	}
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	api := datadogV1.NewDashboardsApi(l.client)
	var (
		mu   sync.Mutex
		out  = make(map[string]string, len(ids))
		wg   sync.WaitGroup
		sem  = make(chan struct{}, tagBackfillConcurrency)
		errN int
	)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d, resp, err := api.GetDashboard(ctx, id)
			l.track(resp)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errN++
				slog.Debug("dashboard tag fetch failed", "id", id, "err", err)
				return
			}
			tags := append([]string(nil), d.GetTags()...)
			sort.Strings(tags)
			out[id] = strings.Join(tags, " ")
		}(id)
	}
	wg.Wait()
	if len(out) == 0 && errN > 0 {
		return nil, fmt.Errorf("all %d dashboard tag fetches failed (rate limit or permissions)", errN)
	}
	if errN > 0 {
		slog.Warn("some dashboard tag fetches failed", "failed", errN, "ok", len(out))
	}
	return out, nil
}
