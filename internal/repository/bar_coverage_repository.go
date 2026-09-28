package repository

import (
	"context"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
	"time"
)

type BarCoverage struct {
	Code      string           `json:"code"`
	Period    domain.BarPeriod `json:"period"`
	Count     int64            `json:"count"`
	FirstDate string           `json:"first_date"`
	LastDate  string           `json:"last_date"`
}
type BarCoverageRepository interface {
	Coverage(context.Context, string, domain.BarPeriod) (BarCoverage, error)
}

func (r *gormBarRepository) Coverage(ctx context.Context, code string, period domain.BarPeriod) (BarCoverage, error) {
	out := BarCoverage{Code: code, Period: period}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := func() *gorm.DB { return tx.Model(&domain.Bar{}).Where("code = ? AND period = ?", code, period) }
		if err := query().Count(&out.Count).Error; err != nil {
			return err
		}
		if out.Count == 0 {
			return nil
		}
		var first, last domain.Bar
		if err := query().Select("date").Order("date ASC").First(&first).Error; err != nil {
			return err
		}
		if err := query().Select("date").Order("date DESC").First(&last).Error; err != nil {
			return err
		}
		out.FirstDate = first.Date.Format("2006-01-02")
		out.LastDate = last.Date.Format("2006-01-02")
		return nil
	})
	return out, err
}
func (r *csvBarRepository) Coverage(ctx context.Context, code string, period domain.BarPeriod) (BarCoverage, error) {
	out := BarCoverage{Code: code, Period: period}
	err := r.walkBounded(ctx, code, period, time.Time{}, time.Time{}, func(bar *domain.Bar) {
		out.Count++
		if out.Count == 1 {
			out.FirstDate = bar.Date.Format("2006-01-02")
		}
		out.LastDate = bar.Date.Format("2006-01-02")
	})
	return out, err
}
