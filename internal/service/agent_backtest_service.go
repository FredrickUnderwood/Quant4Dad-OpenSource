package service

import (
	"context"
	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"math"
	"time"
)

type AgentBacktestInput struct {
	StrategyID      int64   `json:"strategy_id"`
	ExpectedVersion int     `json:"expected_version"`
	CostID          int64   `json:"cost_id"`
	InitialCapital  float64 `json:"initial_capital"`
	StartDate       string  `json:"start_date"`
	EndDate         string  `json:"end_date"`
}

func (s *AgentMutationService) Backtest(ctx context.Context, costSvc *CostService, in AgentBacktestInput) (domain.AgentMutationResult, error) {
	invalid := func() (domain.AgentMutationResult, error) { return domain.AgentMutationResult{}, ErrToolInput }
	if in.StrategyID < 1 || in.ExpectedVersion < 1 || in.CostID < 0 || in.InitialCapital <= 0 || in.InitialCapital > 1e9 || math.IsNaN(in.InitialCapital) {
		return invalid()
	}
	start, err := time.Parse("2006-01-02", in.StartDate)
	if err != nil {
		return invalid()
	}
	end, err := time.Parse("2006-01-02", in.EndDate)
	if err != nil || end.Before(start) || end.Sub(start) > 3660*24*time.Hour {
		return invalid()
	}
	st, err := s.strategy.GetByID(ctx, in.StrategyID)
	if err != nil {
		return domain.AgentMutationResult{}, err
	}
	if st.Version != in.ExpectedVersion {
		return domain.AgentMutationResult{}, domain.ErrResourceConflict
	}
	var body domain.StrategyBody
	if len(st.Body) > 49152 || sonic.Unmarshal(st.Body, &body) != nil {
		return invalid()
	}
	if s.strategy.ValidateAgentContext(ctx, StrategyInput{Name: st.Name, Description: st.Description, Universe: st.Universe, Period: st.Period, Body: body}) != nil {
		return invalid()
	}
	var cost *domain.Cost
	if in.CostID == 0 {
		cost, err = costSvc.GetDefault(ctx)
	} else {
		cost, err = costSvc.GetByID(ctx, in.CostID)
	}
	if err != nil {
		return domain.AgentMutationResult{}, err
	}
	snapshot, err := sonic.MarshalString(domain.AgentBacktestSnapshot{Strategy: *st, Cost: *cost})
	if err != nil || len(snapshot) > 65536 {
		return invalid()
	}
	job := &domain.BacktestJob{StrategyID: st.ID, CostID: cost.ID, InitialCapital: in.InitialCapital, StartDate: start, EndDate: end, AgentRun: true, AgentSnapshot: snapshot}
	return s.execute(ctx, domain.AgentMutation{Kind: "run_backtest", Backtest: job})
}
