package repository

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type NewsRepository struct {
	db *gorm.DB
}

func NewNewsRepository(db *gorm.DB) *NewsRepository {
	return &NewsRepository{db: db}
}

// InsertNew stores a batch of items from one source, deduplicated, and returns those that
// were newly inserted this time, which the collector dispatches to subscribed pipelines. It
// works by first querying the set of already-present external_ids in bulk and then
// inserting only the new items. Not relying on ON CONFLICT keeps it portable across sqlite
// and mysql.
func (r *NewsRepository) InsertNew(ctx context.Context, source string, items []*domain.News) ([]*domain.News, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ExternalID)
	}
	var existing []string
	if err := r.db.WithContext(ctx).Model(&domain.News{}).
		Where("source = ? AND external_id IN ?", source, ids).
		Pluck("external_id", &existing).Error; err != nil {
		logger.L().Error("news existing query failed", zap.String("source", source), zap.Error(err))
		return nil, err
	}
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[e] = struct{}{}
	}

	now := time.Now()
	fresh := make([]*domain.News, 0, len(items))
	dedupInBatch := make(map[string]struct{}, len(items))
	for _, it := range items {
		if _, ok := seen[it.ExternalID]; ok {
			continue
		}
		// A batch can contain duplicates of its own, from source paging or jitter, so
		// deduplicate again.
		if _, ok := dedupInBatch[it.ExternalID]; ok {
			continue
		}
		dedupInBatch[it.ExternalID] = struct{}{}
		it.CreatedAt = now
		fresh = append(fresh, it)
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	if err := r.db.WithContext(ctx).Create(&fresh).Error; err != nil {
		logger.L().Error("news insert failed", zap.String("source", source), zap.Error(err))
		return nil, err
	}
	return fresh, nil
}

// Exists reports whether a given external_id from a source is already stored. The collector
// uses it while paging to tell whether it has caught up with the previous batch. It is a
// point lookup on the unique (source, external_id) index, so it costs very little.
func (r *NewsRepository) Exists(ctx context.Context, source, externalID string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&domain.News{}).
		Where("source = ? AND external_id = ?", source, externalID).
		Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// NewsQuery is the filter for browsing news. Keyword matches the title, via LIKE.
type NewsQuery struct {
	Source  string
	Keyword string
	Limit   int
	Offset  int
}

// List returns news in descending publication-time order along with the total count, for
// the UI's chronological browsing and paging.
func (r *NewsRepository) List(ctx context.Context, q NewsQuery) ([]*domain.News, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domain.News{})
	if q.Source != "" {
		tx = tx.Where("source = ?", q.Source)
	}
	if q.Keyword != "" {
		tx = tx.Where("title LIKE ?", "%"+q.Keyword+"%")
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var items []*domain.News
	if err := tx.Order("published_at DESC, id DESC").Limit(limit).Offset(q.Offset).Find(&items).Error; err != nil {
		logger.L().Error("news list failed", zap.Error(err))
		return nil, 0, err
	}
	return items, total, nil
}

func (r *NewsRepository) GetByID(ctx context.Context, id int64) (*domain.News, error) {
	var n domain.News
	if err := r.db.WithContext(ctx).First(&n, id).Error; err != nil {
		return nil, err
	}
	return &n, nil
}
