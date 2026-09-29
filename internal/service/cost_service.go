package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

type CostService struct {
	repo *repository.CostRepository
}

func NewCostService(repo *repository.CostRepository) *CostService {
	return &CostService{repo: repo}
}

func (s *CostService) EnsureDefault(ctx context.Context) error {
	return s.repo.EnsureDefault(ctx)
}

func (s *CostService) Create(ctx context.Context, c *domain.Cost) error {
	if err := validateCost(c); err != nil {
		return err
	}
	if err := s.repo.Create(ctx, c); err != nil {
		return err
	}
	if c.IsDefault {
		if err := s.repo.SetDefault(ctx, c.ID); err != nil {
			return err
		}
	}
	logger.L().Info("cost created", zap.Int64("id", c.ID), zap.String("name", c.Name))
	return nil
}

func (s *CostService) Update(ctx context.Context, c *domain.Cost) error {
	if err := validateCost(c); err != nil {
		return err
	}
	if c.ID <= 0 {
		return errors.New("cost id must be positive")
	}
	if err := s.repo.Update(ctx, c); err != nil {
		return err
	}
	if c.IsDefault {
		if err := s.repo.SetDefault(ctx, c.ID); err != nil {
			return err
		}
	}
	logger.L().Info("cost updated", zap.Int64("id", c.ID))
	return nil
}

func (s *CostService) GetByID(ctx context.Context, id int64) (*domain.Cost, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *CostService) GetDefault(ctx context.Context) (*domain.Cost, error) {
	return s.repo.GetDefault(ctx)
}

func (s *CostService) List(ctx context.Context) ([]*domain.Cost, error) {
	return s.repo.List(ctx)
}

func (s *CostService) Delete(ctx context.Context, id int64) error {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if c.IsDefault {
		return errors.New("cannot delete the default cost")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	logger.L().Info("cost deleted", zap.Int64("id", id))
	return nil
}

func (s *CostService) SetDefault(ctx context.Context, id int64) error {
	if err := s.repo.SetDefault(ctx, id); err != nil {
		return err
	}
	logger.L().Info("cost default updated", zap.Int64("id", id))
	return nil
}

// Validate at the service boundary so HTTP clients cannot bypass UI checks.
func validateCost(c *domain.Cost) error {
	if c == nil || !utf8.ValidString(c.Name) {
		return errors.New("invalid cost model")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || utf8.RuneCountInString(c.Name) > 64 || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 {
		return errors.New("cost name must contain 1 to 64 characters without control characters")
	}
	for _, value := range []float64{c.CommissionRate, c.MinCommission, c.StampDutyRate, c.SlippageBps} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return errors.New("cost values must be finite and non-negative")
		}
	}
	if c.CommissionRate > 1 || c.StampDutyRate > 1 || c.SlippageBps > 10000 {
		return errors.New("cost rates cannot exceed 1 and slippage cannot exceed 10000 bps")
	}
	return nil
}
