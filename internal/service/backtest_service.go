package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

type BacktestService struct {
	jobs   *repository.BacktestRepository
	trades *repository.TradeRepository
	equity *repository.EquityRepository
}

func NewBacktestService(jobs *repository.BacktestRepository, trades *repository.TradeRepository, equity *repository.EquityRepository) *BacktestService {
	return &BacktestService{jobs: jobs, trades: trades, equity: equity}
}

func (s *BacktestService) CreateJob(ctx context.Context, j *domain.BacktestJob) error {
	return s.jobs.CreateJob(ctx, j)
}

func (s *BacktestService) ClaimAgent(ctx context.Context) (*domain.BacktestJob, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return s.jobs.ClaimAgent(ctx, hex.EncodeToString(nonce[:]))
}
func (s *BacktestService) FailExpiredAgent(ctx context.Context) error {
	return s.jobs.FailExpiredAgent(ctx)
}
func (s *BacktestService) FailAgent(ctx context.Context, job *domain.BacktestJob, code string) error {
	return s.jobs.FailAgent(ctx, job, code)
}
func (s *BacktestService) CompleteAgent(ctx context.Context, job *domain.BacktestJob, result *domain.BacktestResult, trades []*domain.Trade, equity []*domain.EquityPoint) error {
	return s.jobs.CompleteAgent(ctx, job, result, trades, equity)
}

func (s *BacktestService) UpdateJob(ctx context.Context, j *domain.BacktestJob) error {
	return s.jobs.UpdateJob(ctx, j)
}

func (s *BacktestService) GetJob(ctx context.Context, id int64) (*domain.BacktestJob, error) {
	return s.jobs.GetJob(ctx, id)
}

func (s *BacktestService) ListJobs(ctx context.Context, limit int) ([]*domain.BacktestJob, error) {
	return s.jobs.ListJobs(ctx, limit)
}

func (s *BacktestService) GetResult(ctx context.Context, jobID int64) (*domain.BacktestResult, error) {
	return s.jobs.GetResult(ctx, jobID)
}

func (s *BacktestService) SaveResult(ctx context.Context, res *domain.BacktestResult) error {
	return s.jobs.SaveResult(ctx, res)
}

func (s *BacktestService) SaveTrades(ctx context.Context, jobID int64, trades []*domain.Trade) error {
	for _, t := range trades {
		t.JobID = jobID
	}
	if err := s.trades.DeleteByJob(ctx, jobID); err != nil {
		return err
	}
	return s.trades.BulkCreate(ctx, trades)
}

func (s *BacktestService) ListTrades(ctx context.Context, jobID int64) ([]*domain.Trade, error) {
	return s.trades.ListByJob(ctx, jobID)
}

func (s *BacktestService) SaveEquity(ctx context.Context, jobID int64, points []*domain.EquityPoint) error {
	for _, p := range points {
		p.JobID = jobID
	}
	if err := s.equity.DeleteByJob(ctx, jobID); err != nil {
		return err
	}
	return s.equity.BulkCreate(ctx, points)
}

func (s *BacktestService) ListEquity(ctx context.Context, jobID int64) ([]*domain.EquityPoint, error) {
	return s.equity.ListByJob(ctx, jobID)
}
