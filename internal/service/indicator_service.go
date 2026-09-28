package service

import "github.com/quant4dad/internal/indicator"

type IndicatorService struct{}

func NewIndicatorService() *IndicatorService { return &IndicatorService{} }

// List returns metadata for all registered indicators, used by the UI to
// populate the indicator dropdown when composing a strategy.
func (s *IndicatorService) List() []map[string]any {
	return indicator.List()
}
