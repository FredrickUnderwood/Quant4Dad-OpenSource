package service

import (
	"context"
	"github.com/quant4dad/internal/domain"
)

func (s *PipelineService) ListPage(ctx context.Context, beforeID int64, limit int) ([]*domain.Pipeline, error) {
	return s.repo.ListPage(ctx, beforeID, limit)
}

func (s *PipelineService) GetAgent(ctx context.Context, id int64) (*domain.Pipeline, error) {
	return s.repo.GetAgent(ctx, id)
}
func (s *PipelineService) AgentMetadata(ctx context.Context, id int64) (*domain.Pipeline, error) {
	return s.repo.AgentMetadata(ctx, id)
}
