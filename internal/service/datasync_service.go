package service

import (
	"context"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

type DataSyncService struct {
	repo *repository.DataSyncRepository
}

func NewDataSyncService(repo *repository.DataSyncRepository) *DataSyncService {
	return &DataSyncService{repo: repo}
}

func (s *DataSyncService) Create(ctx context.Context, t *domain.DataSyncTask) error {
	return s.repo.Create(ctx, t)
}

func (s *DataSyncService) Update(ctx context.Context, t *domain.DataSyncTask) error {
	return s.repo.Update(ctx, t)
}

func (s *DataSyncService) GetByID(ctx context.Context, id int64) (*domain.DataSyncTask, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *DataSyncService) List(ctx context.Context, limit int) ([]*domain.DataSyncTask, error) {
	return s.repo.List(ctx, limit)
}

func (s *DataSyncService) IncrementProgress(ctx context.Context, id int64, doneDelta, failedDelta int) error {
	return s.repo.IncrementProgress(ctx, id, doneDelta, failedDelta)
}

func (s *DataSyncService) RecordFailure(ctx context.Context, f *domain.DataSyncFailure) error {
	return s.repo.RecordFailure(ctx, f)
}

func (s *DataSyncService) ListFailures(ctx context.Context, taskID int64, limit int) ([]*domain.DataSyncFailure, error) {
	return s.repo.ListFailures(ctx, taskID, limit)
}
