package service

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

var ErrToolInput = errors.New("tool_invalid_arguments")
var ErrToolUnavailable = errors.New("tool_unavailable")
var instrumentCode = regexp.MustCompile(`^(sh|sz|bj)\.[0-9]{6}$`)

type QueryKlineInput struct {
	Code   string `json:"code"`
	Period string `json:"period"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Limit  *int   `json:"limit"`
}
type QueryKlineResult struct {
	Code           string        `json:"code"`
	Period         string        `json:"period"`
	Count          int           `json:"count"`
	Bars           []*domain.Bar `json:"bars"`
	Truncated      bool          `json:"truncated"`
	DataAsOf       string        `json:"data_as_of"`
	PreviousBar    *domain.Bar   `json:"previous_bar,omitempty"`
	RequestedStart string        `json:"requested_start,omitempty"`
	RequestedEnd   string        `json:"requested_end,omitempty"`
}

func queryPeriod(code, period string) (domain.BarPeriod, error) {
	if !instrumentCode.MatchString(code) {
		return "", ErrToolInput
	}
	if period == "" {
		period = "1d"
	}
	p := domain.BarPeriod(period)
	if p != domain.Bar1d && p != domain.Bar1w && p != domain.Bar1mo {
		return "", ErrToolInput
	}
	return p, nil
}
func (s *InstrumentService) QueryKline(ctx context.Context, input QueryKlineInput) (QueryKlineResult, error) {
	var result QueryKlineResult
	period, err := queryPeriod(input.Code, input.Period)
	if err != nil {
		return result, err
	}
	limit := 120
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 500 {
		return result, ErrToolInput
	}
	var start, end time.Time
	if input.Start != "" {
		start, err = time.Parse("2006-01-02", input.Start)
		if err != nil {
			return result, ErrToolInput
		}
	}
	if input.End != "" {
		end, err = time.Parse("2006-01-02", input.End)
		if err != nil {
			return result, ErrToolInput
		}
	}
	if !start.IsZero() && !end.IsZero() && start.After(end) {
		return result, ErrToolInput
	}
	repo, ok := s.barRepo.(repository.BoundedBarRepository)
	if !ok {
		return result, ErrToolUnavailable
	}
	bars, err := repo.RangeLatest(ctx, input.Code, period, start, end, limit+1)
	if err != nil {
		return result, err
	}
	result = QueryKlineResult{Code: input.Code, Period: string(period), Truncated: len(bars) > limit, Bars: bars, RequestedStart: input.Start, RequestedEnd: input.End}
	if result.Truncated {
		result.PreviousBar = bars[len(bars)-limit-1]
		result.Bars = bars[len(bars)-limit:]
	}
	if result.Bars == nil {
		result.Bars = []*domain.Bar{}
	}
	result.Count = len(result.Bars)
	if result.Count > 0 {
		result.DataAsOf = result.Bars[result.Count-1].Date.Format("2006-01-02")
		if result.PreviousBar == nil && !start.IsZero() {
			previous, err := repo.RangeLatest(ctx, input.Code, period, time.Time{}, result.Bars[0].Date.AddDate(0, 0, -1), 1)
			if err != nil {
				return QueryKlineResult{}, err
			}
			if len(previous) > 0 {
				result.PreviousBar = previous[len(previous)-1]
			}
		}
	}
	return result, nil
}
func (s *InstrumentService) LatestBar(ctx context.Context, code, period string) (*domain.Bar, error) {
	p, err := queryPeriod(code, period)
	if err != nil {
		return nil, err
	}
	repo, ok := s.barRepo.(repository.BoundedBarRepository)
	if !ok {
		return nil, ErrToolUnavailable
	}
	bars, err := repo.RangeLatest(ctx, code, p, time.Time{}, time.Time{}, 1)
	if err != nil || len(bars) == 0 {
		return nil, err
	}
	return bars[0], nil
}
func ValidInstrumentCode(code string) bool { return instrumentCode.MatchString(code) }

func (s *InstrumentService) DataCoverage(ctx context.Context, code, period string) (repository.BarCoverage, error) {
	p, err := queryPeriod(code, period)
	if err != nil {
		return repository.BarCoverage{}, err
	}
	repo, ok := s.barRepo.(repository.BarCoverageRepository)
	if !ok {
		return repository.BarCoverage{}, ErrToolUnavailable
	}
	return repo.Coverage(ctx, code, p)
}
