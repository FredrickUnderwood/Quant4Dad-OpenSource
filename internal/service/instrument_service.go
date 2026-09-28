package service

import (
	"context"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

type InstrumentService struct {
	repo    *repository.InstrumentRepository
	barRepo repository.BarRepository
}

func NewInstrumentService(repo *repository.InstrumentRepository, barRepo repository.BarRepository) *InstrumentService {
	return &InstrumentService{repo: repo, barRepo: barRepo}
}

func (s *InstrumentService) RangeBars(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	return s.barRepo.Range(ctx, code, period, start, end)
}

func (s *InstrumentService) Upsert(ctx context.Context, items []*domain.Instrument) error {
	return s.repo.Upsert(ctx, items)
}

func (s *InstrumentService) List(ctx context.Context, f repository.ListInstrumentsFilter) ([]*domain.Instrument, int64, error) {
	return s.repo.List(ctx, f)
}

func (s *InstrumentService) ListAllCodes(ctx context.Context) ([]string, error) {
	return s.repo.ListAllCodes(ctx)
}

func (s *InstrumentService) GetByCode(ctx context.Context, code string) (*domain.Instrument, error) {
	return s.repo.GetByCode(ctx, code)
}
