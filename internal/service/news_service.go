package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

// knownNewsSources is populated from the datasource.NewsSource registry during
// startup (see cmd/quant4dad/main.go). It is used both to fill in configuration
// defaults and to validate pipeline subscriptions.
//
// Written exactly once during startup wiring — before any goroutine starts or any
// request is served — and read-only thereafter.
var knownNewsSources []string

// SetKnownNewsSources fills in the list of usable news sources; main calls it once
// during wiring with the registry's contents. The argument is deduplicated, sorted
// and copied, so the caller mutating the slice afterwards has no effect here.
func SetKnownNewsSources(names []string) {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	knownNewsSources = out
}

// KnownNewsSources returns a copy of the usable news source list, in ascending order.
func KnownNewsSources() []string {
	return append([]string(nil), knownNewsSources...)
}

// IsKnownNewsSource reports whether a source name is supported.
func IsKnownNewsSource(s string) bool {
	for _, k := range knownNewsSources {
		if k == s {
			return true
		}
	}
	return false
}

// defaultSourceIntervalSeconds is the collection interval used for a source that
// has not been configured.
const defaultSourceIntervalSeconds = 60

// minBaseIntervalSeconds is the floor on the collector's base tick, so a too-small
// setting cannot spin and saturate the CPU or upstream.
const minBaseIntervalSeconds = 5

// NewsSourceSetting is one news source's collection config (stored in the setting
// table under news.sources).
type NewsSourceSetting struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
}

// NewsCollectorConfig is the collector's global config (stored in the setting table
// under news.collector). Enabled is the master switch; BaseIntervalSeconds is the
// base poll tick — the collector wakes up that often, re-reads config, and fetches
// from sources that are due. With the switch off it still wakes on that tick and
// re-checks, so turning it back on takes effect immediately.
type NewsCollectorConfig struct {
	Enabled             bool `json:"enabled"`
	BaseIntervalSeconds int  `json:"base_interval_seconds"`
}

type NewsService struct {
	newsRepo    *repository.NewsRepository
	settingRepo *repository.SettingRepository

	// Fallbacks for the global config when the setting table has no row yet; they
	// come from config.yaml's news section and act as the initial seed.
	defaultEnabled      bool
	defaultBaseInterval int
}

func NewNewsService(
	newsRepo *repository.NewsRepository,
	settingRepo *repository.SettingRepository,
	defaultEnabled bool,
	defaultBaseIntervalSeconds int,
) *NewsService {
	if defaultBaseIntervalSeconds < minBaseIntervalSeconds {
		defaultBaseIntervalSeconds = 30
	}
	return &NewsService{
		newsRepo:            newsRepo,
		settingRepo:         settingRepo,
		defaultEnabled:      defaultEnabled,
		defaultBaseInterval: defaultBaseIntervalSeconds,
	}
}

// GetCollectorConfig returns the collector's global config, falling back to the
// config file's seed defaults when the setting table has no row.
func (s *NewsService) GetCollectorConfig(ctx context.Context) (NewsCollectorConfig, error) {
	cfg := NewsCollectorConfig{Enabled: s.defaultEnabled, BaseIntervalSeconds: s.defaultBaseInterval}
	row, err := s.settingRepo.Get(ctx, domain.SettingKeyNewsCollector)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(row.Value) > 0 {
		if uErr := sonic.Unmarshal(row.Value, &cfg); uErr != nil {
			return cfg, uErr
		}
	}
	if cfg.BaseIntervalSeconds < minBaseIntervalSeconds {
		cfg.BaseIntervalSeconds = s.defaultBaseInterval
	}
	return cfg, nil
}

// SetCollectorConfig overwrites the collector's global config. The base tick is
// floor-protected.
func (s *NewsService) SetCollectorConfig(ctx context.Context, cfg NewsCollectorConfig) error {
	if cfg.BaseIntervalSeconds < minBaseIntervalSeconds {
		cfg.BaseIntervalSeconds = s.defaultBaseInterval
	}
	value, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Upsert(ctx, domain.SettingKeyNewsCollector, value); err != nil {
		return err
	}
	logger.L().Info("news collector config updated",
		zap.Bool("enabled", cfg.Enabled), zap.Int("base_interval_seconds", cfg.BaseIntervalSeconds))
	return nil
}

func (s *NewsService) ListNews(ctx context.Context, q repository.NewsQuery) ([]*domain.News, int64, error) {
	return s.newsRepo.List(ctx, q)
}

func (s *NewsService) GetNews(ctx context.Context, id int64) (*domain.News, error) {
	return s.newsRepo.GetByID(ctx, id)
}

// GetSourceConfigs returns the collection config for every known source, filling in
// defaults (enabled, default interval) for sources not explicitly configured in the
// setting table, so the UI always receives a complete list.
func (s *NewsService) GetSourceConfigs(ctx context.Context) (map[string]NewsSourceSetting, error) {
	stored := map[string]NewsSourceSetting{}
	row, err := s.settingRepo.Get(ctx, domain.SettingKeyNewsSources)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil && len(row.Value) > 0 {
		if uErr := sonic.Unmarshal(row.Value, &stored); uErr != nil {
			return nil, uErr
		}
	}
	out := make(map[string]NewsSourceSetting, len(knownNewsSources))
	for _, name := range knownNewsSources {
		if cfg, ok := stored[name]; ok {
			if cfg.IntervalSeconds <= 0 {
				cfg.IntervalSeconds = defaultSourceIntervalSeconds
			}
			out[name] = cfg
		} else {
			out[name] = NewsSourceSetting{Enabled: true, IntervalSeconds: defaultSourceIntervalSeconds}
		}
	}
	return out, nil
}

// SetSourceConfigs overwrites the per-source collection config wholesale. Only known
// sources are accepted, and the interval has a 5s floor to avoid hammering upstream.
func (s *NewsService) SetSourceConfigs(ctx context.Context, cfgs map[string]NewsSourceSetting) error {
	clean := make(map[string]NewsSourceSetting, len(cfgs))
	for name, c := range cfgs {
		if !IsKnownNewsSource(name) {
			return fmt.Errorf("unknown news source %q, registered sources: %v", name, knownNewsSources)
		}
		if c.IntervalSeconds < 5 {
			c.IntervalSeconds = defaultSourceIntervalSeconds
		}
		clean[name] = c
	}
	value, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Upsert(ctx, domain.SettingKeyNewsSources, value); err != nil {
		return err
	}
	logger.L().Info("news source configs updated", zap.Int("count", len(clean)))
	return nil
}
