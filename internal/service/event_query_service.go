package service

import (
	"context"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

type EventQueryService struct{ repo *repository.EventRepository }

func NewEventQueryService(repo *repository.EventRepository) *EventQueryService {
	return &EventQueryService{repo: repo}
}
func (s *EventQueryService) List(ctx context.Context, query repository.EventQuery) ([]*domain.Event, int64, error) {
	return s.repo.ListAgent(ctx, query)
}
