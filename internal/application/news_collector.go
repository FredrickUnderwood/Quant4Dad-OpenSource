// Package application: the news collector is orchestrated here — polling each source
// per its config, storing deduplicated items, and feeding new items as events into the
// pipelines subscribed to that source (reusing PipelineApp.Ingest).
package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/datasource"
	"github.com/quant4dad/internal/service"
)

// NewsSourceStatus is what the UI sees: source name, enabled flag, collection
// interval, and the last poll's time, fetched count, new count and error. It is
// merged with the service layer's source config for display.
type NewsSourceStatus struct {
	Source          string     `json:"source"`
	Enabled         bool       `json:"enabled"`
	IntervalSeconds int        `json:"interval_seconds"`
	LastPolledAt    *time.Time `json:"last_polled_at"`
	LastFetched     int        `json:"last_fetched"`
	LastNew         int        `json:"last_new"`
	LastError       string     `json:"last_error,omitempty"`
}

type sourceRuntime struct {
	lastPolledAt *time.Time
	lastFetched  int
	lastNew      int
	lastErr      string
}

// defaultBaseInterval is the fallback tick used when reading config fails.
const defaultBaseInterval = 30 * time.Second

// NewsCollector polls from a single long-running goroutine, taking all of its config
// from the setting table (editable from the UI). On each wake-up it re-reads the
// global config: with the master switch off it merely idles and re-checks on the base
// tick, so switching it back on takes effect immediately; with it on, it re-reads each
// source's config and fetches from every source that is enabled and whose own interval
// has elapsed. The base tick is re-read every tick too, so re-timing needs no restart.
type NewsCollector struct {
	sources     map[string]datasource.NewsSource
	newsRepo    *repository.NewsRepository
	subRepo     *repository.NewsSubscriptionRepository
	newsSvc     *service.NewsService
	pipelineApp *PipelineApp

	mu      sync.Mutex
	runtime map[string]*sourceRuntime

	// pollLocks serializes fetching per source, so a scheduled poll and a manual
	// "collect now" hitting the same source concurrently cannot have their two InsertNew
	// batches collide on the unique index. One lock per source, initialized from the
	// registered sources at construction.
	pollLocks map[string]*sync.Mutex
}

func NewNewsCollector(
	sources map[string]datasource.NewsSource,
	newsRepo *repository.NewsRepository,
	subRepo *repository.NewsSubscriptionRepository,
	newsSvc *service.NewsService,
	pipelineApp *PipelineApp,
) *NewsCollector {
	locks := make(map[string]*sync.Mutex, len(sources))
	for name := range sources {
		locks[name] = &sync.Mutex{}
	}
	return &NewsCollector{
		sources:     sources,
		newsRepo:    newsRepo,
		subRepo:     subRepo,
		newsSvc:     newsSvc,
		pipelineApp: pipelineApp,
		runtime:     make(map[string]*sourceRuntime),
		pollLocks:   locks,
	}
}

// Start launches the collector goroutine — long-running, with start/stop controlled at
// runtime by the master switch in the setting table — and returns a cancel for shutdown.
func (c *NewsCollector) Start() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go c.loop(ctx)
	return cancel
}

// loop is self-scheduling: each round re-reads the global config to decide whether to
// collect and what the next tick is, then sleeps until then.
func (c *NewsCollector) loop(ctx context.Context) {
	logger.L().Info("news collector started")
	for {
		base := defaultBaseInterval
		cc, err := c.newsSvc.GetCollectorConfig(ctx)
		if err != nil {
			logger.L().Error("news collector load global config failed", zap.Error(err))
		} else {
			if cc.BaseIntervalSeconds > 0 {
				base = time.Duration(cc.BaseIntervalSeconds) * time.Second
			}
			if cc.Enabled {
				c.tick(ctx)
			}
		}
		timer := time.NewTimer(base)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.L().Info("news collector stopped")
			return
		case <-timer.C:
		}
	}
}

// tick re-reads the per-source config and fetches concurrently from every source that
// is enabled and due.
func (c *NewsCollector) tick(ctx context.Context) {
	cfgs, err := c.newsSvc.GetSourceConfigs(ctx)
	if err != nil {
		logger.L().Error("news collector load configs failed", zap.Error(err))
		return
	}
	now := time.Now()
	var wg sync.WaitGroup
	for name, src := range c.sources {
		cfg, ok := cfgs[name]
		if !ok || !cfg.Enabled {
			continue
		}
		if !c.due(name, cfg.IntervalSeconds, now) {
			continue
		}
		wg.Add(1)
		go func(name string, src datasource.NewsSource) {
			defer wg.Done()
			c.pollSource(ctx, name, src)
		}(name, src)
	}
	wg.Wait()
}

// due reports whether a source has reached its next collection time.
func (c *NewsCollector) due(source string, intervalSeconds int, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.runtime[source]
	if rt == nil || rt.lastPolledAt == nil {
		return true
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 60
	}
	return !now.Before(rt.lastPolledAt.Add(time.Duration(intervalSeconds) * time.Second))
}

// PollNow collects from the named source immediately — a manual trigger that ignores
// the enabled flag and the interval — and returns how many new items were stored.
func (c *NewsCollector) PollNow(ctx context.Context, source string) (int, error) {
	src, ok := c.sources[source]
	if !ok {
		return 0, errors.New("unknown news source: " + source)
	}
	return c.pollSource(ctx, source, src)
}

// pollSource fetches, stores deduplicated items, dispatches to subscribed pipelines and
// records the run state. It returns how many new items were stored.
func (c *NewsCollector) pollSource(ctx context.Context, name string, src datasource.NewsSource) (int, error) {
	if lock := c.pollLocks[name]; lock != nil {
		lock.Lock()
		defer lock.Unlock()
	}
	now := time.Now()
	// seen lets a source page from the newest end and stop once it has caught up with the
	// previous batch, so a burst larger than one page's capacity is not missed. A failed
	// lookup is treated as "unknown" (paging continues to the cap); InsertNew still
	// deduplicates, so nothing is stored twice.
	seen := func(externalID string) bool {
		exists, qErr := c.newsRepo.Exists(ctx, name, externalID)
		if qErr != nil {
			logger.L().Warn("news seen check failed",
				zap.String("source", name), zap.String("external_id", externalID), zap.Error(qErr))
			return false
		}
		return exists
	}
	items, err := src.Fetch(ctx, seen)
	if err != nil {
		c.record(name, &now, 0, 0, err.Error())
		logger.L().Warn("news fetch failed", zap.String("source", name), zap.Error(err))
		return 0, err
	}
	fresh, err := c.newsRepo.InsertNew(ctx, name, items)
	if err != nil {
		c.record(name, &now, len(items), 0, err.Error())
		return 0, err
	}
	c.record(name, &now, len(items), len(fresh), "")
	if len(fresh) > 0 {
		logger.L().Info("news collected",
			zap.String("source", name), zap.Int("fetched", len(items)), zap.Int("new", len(fresh)))
		c.dispatch(ctx, name, fresh)
	}
	return len(fresh), nil
}

// dispatch feeds new items into the pipelines subscribed to that source. Ingest itself
// skips pipelines that are not enabled (returning ErrPipelineDisabled), so there is no
// need to check status here.
func (c *NewsCollector) dispatch(ctx context.Context, source string, items []*domain.News) {
	pipelineIDs, err := c.subRepo.ListPipelineIDsBySource(ctx, source)
	if err != nil {
		logger.L().Error("news dispatch list subscribers failed", zap.String("source", source), zap.Error(err))
		return
	}
	if len(pipelineIDs) == 0 {
		return
	}
	for _, item := range items {
		payload := newsPayload(item)
		for _, pid := range pipelineIDs {
			if _, err := c.pipelineApp.Ingest(ctx, pid, source, payload); err != nil {
				if errors.Is(err, ErrPipelineDisabled) {
					continue
				}
				logger.L().Error("news ingest failed",
					zap.String("source", source), zap.Int64("pipeline_id", pid),
					zap.String("external_id", item.ExternalID), zap.Error(err))
			}
		}
	}
}

func newsPayload(n *domain.News) map[string]any {
	return map[string]any{
		"source":       n.Source,
		"external_id":  n.ExternalID,
		"title":        n.Title,
		"content":      n.Content,
		"url":          n.URL,
		"published_at": n.PublishedAt.Format(time.RFC3339),
	}
}

func (c *NewsCollector) record(source string, polledAt *time.Time, fetched, fresh int, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.runtime[source]
	if rt == nil {
		rt = &sourceRuntime{}
		c.runtime[source] = rt
	}
	rt.lastPolledAt = polledAt
	rt.lastFetched = fetched
	rt.lastNew = fresh
	rt.lastErr = errMsg
}

// Status merges the source config with the runtime state for display in the UI.
func (c *NewsCollector) Status(ctx context.Context) ([]NewsSourceStatus, error) {
	cfgs, err := c.newsSvc.GetSourceConfigs(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	names := service.KnownNewsSources()
	out := make([]NewsSourceStatus, 0, len(names))
	for _, name := range names {
		cfg := cfgs[name]
		st := NewsSourceStatus{
			Source:          name,
			Enabled:         cfg.Enabled,
			IntervalSeconds: cfg.IntervalSeconds,
		}
		if rt := c.runtime[name]; rt != nil {
			st.LastPolledAt = rt.lastPolledAt
			st.LastFetched = rt.lastFetched
			st.LastNew = rt.lastNew
			st.LastError = rt.lastErr
		}
		out = append(out, st)
	}
	return out, nil
}
