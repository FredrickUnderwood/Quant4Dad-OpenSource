package repository

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type NewsSubscriptionRepository struct {
	db *gorm.DB
}

func NewNewsSubscriptionRepository(db *gorm.DB) *NewsSubscriptionRepository {
	return &NewsSubscriptionRepository{db: db}
}

// ListPipelineIDsBySource returns the IDs of every pipeline subscribed to a source,
// regardless of whether they are enabled — the collector filters on pipeline.status when it
// dispatches.
func (r *NewsSubscriptionRepository) ListPipelineIDsBySource(ctx context.Context, source string) ([]int64, error) {
	var ids []int64
	if err := r.db.WithContext(ctx).Model(&domain.NewsSubscription{}).
		Where("source = ?", source).Pluck("pipeline_id", &ids).Error; err != nil {
		logger.L().Error("news sub list by source failed", zap.String("source", source), zap.Error(err))
		return nil, err
	}
	return ids, nil
}

// ListSourcesByPipeline returns the sources one pipeline subscribes to.
func (r *NewsSubscriptionRepository) ListSourcesByPipeline(ctx context.Context, pipelineID int64) ([]string, error) {
	var sources []string
	if err := r.db.WithContext(ctx).Model(&domain.NewsSubscription{}).
		Where("pipeline_id = ?", pipelineID).Pluck("source", &sources).Error; err != nil {
		return nil, err
	}
	return sources, nil
}

// ReplaceForPipeline overwrites one pipeline's subscriptions wholesale. Pass tx to join the
// pipeline's save transaction.
func (r *NewsSubscriptionRepository) ReplaceForPipeline(tx *gorm.DB, pipelineID int64, sources []string) error {
	if err := tx.Where("pipeline_id = ?", pipelineID).Delete(&domain.NewsSubscription{}).Error; err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([]domain.NewsSubscription, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		rows = append(rows, domain.NewsSubscription{PipelineID: pipelineID, Source: s, CreatedAt: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}

// DeleteByPipeline removes all of one pipeline's subscriptions. Pass tx to join the delete
// transaction.
func (r *NewsSubscriptionRepository) DeleteByPipeline(tx *gorm.DB, pipelineID int64) error {
	return tx.Where("pipeline_id = ?", pipelineID).Delete(&domain.NewsSubscription{}).Error
}
