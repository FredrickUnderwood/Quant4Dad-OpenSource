package repository

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type PipelineRepository struct {
	db   *gorm.DB
	subs *NewsSubscriptionRepository
}

func NewPipelineRepository(db *gorm.DB) *PipelineRepository {
	return &PipelineRepository{db: db, subs: NewNewsSubscriptionRepository(db)}
}

// Create writes the pipeline along with its nodes and edges inside one transaction.
func (r *PipelineRepository) Create(ctx context.Context, p *domain.Pipeline) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(p).Error; err != nil {
			return err
		}
		return r.saveChildren(tx, p)
	})
	if err != nil {
		logger.L().Error("pipeline create failed", zap.String("name", p.Name), zap.Error(err))
	}
	return err
}

// Update overwrites the nodes and edges wholesale and bumps the version.
func (r *PipelineRepository) Update(ctx context.Context, p *domain.Pipeline) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&domain.Pipeline{}).Where("id = ? AND version = ?", p.ID, p.Version).Updates(map[string]any{
			"name":        p.Name,
			"description": p.Description,
			"status":      p.Status,
			"version":     gorm.Expr("version + 1"),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return domain.ErrResourceConflict
		}
		if err := tx.Where("pipeline_id = ?", p.ID).Delete(&domain.PipelineNode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("pipeline_id = ?", p.ID).Delete(&domain.PipelineEdge{}).Error; err != nil {
			return err
		}
		return r.saveChildren(tx, p)
	})
	if err != nil && !errors.Is(err, domain.ErrResourceConflict) {
		logger.L().Error("pipeline update failed", zap.Int64("id", p.ID), zap.Error(err))
	}
	if err == nil {
		p.Version++
	}
	return err
}

func (r *PipelineRepository) saveChildren(tx *gorm.DB, p *domain.Pipeline) error {
	for i := range p.Nodes {
		p.Nodes[i].ID = 0
		p.Nodes[i].PipelineID = p.ID
	}
	for i := range p.Edges {
		p.Edges[i].ID = 0
		p.Edges[i].PipelineID = p.ID
	}
	if len(p.Nodes) > 0 {
		if err := tx.Create(&p.Nodes).Error; err != nil {
			return err
		}
	}
	if len(p.Edges) > 0 {
		if err := tx.Create(&p.Edges).Error; err != nil {
			return err
		}
	}
	// News source subscriptions are overwritten wholesale in the same transaction as the
	// nodes and edges.
	return r.subs.ReplaceForPipeline(tx, p.ID, p.Sources)
}

// GetByID loads a pipeline along with its nodes and edges.
func (r *PipelineRepository) GetByID(ctx context.Context, id int64) (*domain.Pipeline, error) {
	var p domain.Pipeline
	// Keep the version and all children in one database snapshot.
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&p, id).Error; err != nil {
			return err
		}
		if err := tx.Where("pipeline_id = ?", id).Order("id ASC").Find(&p.Nodes).Error; err != nil {
			return err
		}
		if err := tx.Where("pipeline_id = ?", id).Order("id ASC").Find(&p.Edges).Error; err != nil {
			return err
		}
		var err error
		p.Sources, err = NewNewsSubscriptionRepository(tx).ListSourcesByPipeline(ctx, id)
		return err
	})
	if err != nil {
		logger.L().Error("pipeline get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return &p, nil
}

func (r *PipelineRepository) List(ctx context.Context) ([]*domain.Pipeline, error) {
	var items []*domain.Pipeline
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error; err != nil {
		logger.L().Error("pipeline list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

func (r *PipelineRepository) UpdateStatus(ctx context.Context, id int64, status string, expectedVersion int) error {
	res := r.db.WithContext(ctx).Model(&domain.Pipeline{}).Where("id = ? AND version = ?", id, expectedVersion).
		Updates(map[string]any{"status": status, "version": gorm.Expr("version + 1")})
	if res.Error != nil {
		logger.L().Error("pipeline status update failed", zap.Int64("id", id), zap.Error(res.Error))
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrResourceConflict
	}
	return nil
}

// Delete removes a pipeline along with its nodes and edges.
func (r *PipelineRepository) Delete(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pipeline_id = ?", id).Delete(&domain.PipelineNode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("pipeline_id = ?", id).Delete(&domain.PipelineEdge{}).Error; err != nil {
			return err
		}
		if err := r.subs.DeleteByPipeline(tx, id); err != nil {
			return err
		}
		return tx.Delete(&domain.Pipeline{}, id).Error
	})
	if err != nil {
		logger.L().Error("pipeline delete failed", zap.Int64("id", id), zap.Error(err))
	}
	return err
}
