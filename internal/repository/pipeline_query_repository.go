package repository

import (
	"context"
	"errors"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

func (r *PipelineRepository) ListPage(ctx context.Context, beforeID int64, limit int) ([]*domain.Pipeline, error) {
	if beforeID < 0 || limit < 1 || limit > 101 {
		return nil, errors.New("pipeline_query_limit_invalid")
	}
	q := r.db.WithContext(ctx)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	items := make([]*domain.Pipeline, 0)
	err := q.Select("id,version,SUBSTR(name,1,128) AS name,SUBSTR(description,1,512) AS description,status,created_at,updated_at").Order("id DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *PipelineRepository) AgentMetadata(ctx context.Context, id int64) (*domain.Pipeline, error) {
	var p domain.Pipeline
	err := r.db.WithContext(ctx).Select("id,version,status").First(&p, id).Error
	return &p, err
}

// GetAgent bounds allocations before decoding legacy product data and rejects
// oversized definitions instead of presenting an incomplete edit base.
func (r *PipelineRepository) GetAgent(ctx context.Context, id int64) (*domain.Pipeline, error) {
	var p domain.Pipeline
	oversized := errors.New("pipeline_query_result_too_large")
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Select("id,version,SUBSTR(name,1,129) AS name,SUBSTR(description,1,4097) AS description,status,created_at,updated_at").First(&p, id).Error; err != nil {
			return err
		}
		if err := tx.Select("id,pipeline_id,SUBSTR(node_key,1,65) AS node_key,SUBSTR(type,1,129) AS type,SUBSTR(name,1,129) AS name,SUBSTR(config,1,16385) AS config,pos_x,pos_y").Where("pipeline_id = ?", id).Order("id ASC").Limit(65).Find(&p.Nodes).Error; err != nil {
			return err
		}
		if err := tx.Select("id,pipeline_id,SUBSTR(from_node_key,1,65) AS from_node_key,SUBSTR(to_node_key,1,65) AS to_node_key,SUBSTR(`condition`,1,4097) AS `condition`").Where("pipeline_id = ?", id).Order("id ASC").Limit(129).Find(&p.Edges).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.NewsSubscription{}).Where("pipeline_id = ?", id).Order("source ASC").Limit(33).Pluck("SUBSTR(source,1,65)", &p.Sources).Error; err != nil {
			return err
		}
		if len(p.Name) > 128 || len(p.Description) > 4096 || len(p.Nodes) > 64 || len(p.Edges) > 128 || len(p.Sources) > 32 {
			return oversized
		}
		for _, n := range p.Nodes {
			if len(n.NodeKey) > 64 || len(n.Type) > 128 || len(n.Name) > 128 || len(n.Config) > 16384 {
				return oversized
			}
		}
		for _, e := range p.Edges {
			if len(e.FromNodeKey) > 64 || len(e.ToNodeKey) > 64 || len(e.Condition) > 4096 {
				return oversized
			}
		}
		for _, source := range p.Sources {
			if len(source) > 64 {
				return oversized
			}
		}
		return nil
	})
	return &p, err
}
