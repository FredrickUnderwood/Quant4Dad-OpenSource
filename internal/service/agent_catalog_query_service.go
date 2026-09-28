package service

import (
	"context"
	"github.com/quant4dad/internal/repository"
)

type AgentCatalogQueryService struct {
	repo *repository.AgentCatalogQueryRepository
}

func NewAgentCatalogQueryService(repo *repository.AgentCatalogQueryRepository) *AgentCatalogQueryService {
	return &AgentCatalogQueryService{repo: repo}
}
func (s *AgentCatalogQueryService) List(ctx context.Context, kind string, before int64, limit int) (any, error) {
	if before < 0 || before > 9007199254740991 || limit < 1 || limit > 101 {
		return nil, ErrToolInput
	}
	return s.repo.List(ctx, kind, before, limit)
}
func (s *AgentCatalogQueryService) Get(ctx context.Context, kind string, id int64) (any, error) {
	if id < 1 || id > 9007199254740991 {
		return nil, ErrToolInput
	}
	return s.repo.Get(ctx, kind, id)
}
