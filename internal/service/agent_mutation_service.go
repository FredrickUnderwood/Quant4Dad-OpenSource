package service

import (
	"context"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	appLog "github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"go.uber.org/zap"
)

type agentExecutionKey struct{}
type AgentExecution struct {
	Audit    domain.AgentToolAudit
	Approval domain.AgentApprovalReceipt
}

func WithAgentExecution(ctx context.Context, audit domain.AgentToolAudit, approval domain.AgentApprovalReceipt) context.Context {
	return context.WithValue(ctx, agentExecutionKey{}, AgentExecution{audit, approval})
}
func AgentExecutionFromContext(ctx context.Context) (AgentExecution, bool) {
	v, ok := ctx.Value(agentExecutionKey{}).(AgentExecution)
	return v, ok && v.Audit.ID != ""
}

type AgentMutationService struct {
	repo     *repository.AgentMutationRepository
	strategy *StrategyService
	pipeline *PipelineService
}

func NewAgentMutationService(repo *repository.AgentMutationRepository, strategy *StrategyService, pipeline *PipelineService) *AgentMutationService {
	return &AgentMutationService{repo, strategy, pipeline}
}
func (s *AgentMutationService) execute(ctx context.Context, m domain.AgentMutation) (domain.AgentMutationResult, error) {
	execution, ok := AgentExecutionFromContext(ctx)
	if !ok || execution.Audit.ToolName != m.Kind {
		return domain.AgentMutationResult{}, ErrAgentApproval
	}
	fields := []zap.Field{
		zap.String("tool", m.Kind),
		zap.String("session_id", execution.Audit.SessionID),
		zap.String("run_id", execution.Audit.RunID),
		zap.String("tool_call_id", execution.Audit.ToolCallID),
	}
	if m.Backtest != nil {
		fields = append(fields, zap.Int64("strategy_id", m.Backtest.StrategyID), zap.Int64("cost_id", m.Backtest.CostID))
	}
	appLog.Info(ctx, "agent product mutation started", fields...)
	started := time.Now()
	result, err := s.repo.Execute(ctx, execution.Audit, execution.Approval, m)
	fields = append(fields, zap.Duration("latency", time.Since(started)))
	if err == nil {
		appLog.Info(ctx, "agent product mutation committed", append(fields,
			zap.Int64("resource_id", result.ID), zap.Int("version", result.Version),
			zap.Int64("job_id", result.JobID), zap.String("status", result.Status))...)
	}
	return result, err
}
func (s *AgentMutationService) Strategy(ctx context.Context, id int64, version int, in StrategyInput, validationID string) (domain.AgentMutationResult, error) {
	if err := s.strategy.verifyValidation(ctx, validationID, in); err != nil {
		execution, _ := AgentExecutionFromContext(ctx)
		appLog.Info(ctx, "agent strategy validation rejected", zap.String("run_id", execution.Audit.RunID), zap.String("tool_call_id", execution.Audit.ToolCallID), zap.String("code", err.Error()))
		return domain.AgentMutationResult{}, err
	}
	if err := s.strategy.ValidateAgentContext(ctx, in); err != nil {
		return domain.AgentMutationResult{}, err
	}
	body, err := sonic.Marshal(in.Body)
	if err != nil {
		return domain.AgentMutationResult{}, ErrToolInput
	}
	st := &domain.Strategy{ID: id, Version: version, Name: in.Name, Description: in.Description, Universe: domain.StringSlice(in.Universe), Period: in.Period, Body: body}
	kind := "create_strategy"
	if id > 0 {
		if version < 1 {
			return domain.AgentMutationResult{}, ErrToolInput
		}
		kind = "update_strategy"
	}
	return s.execute(ctx, domain.AgentMutation{Kind: kind, Strategy: st})
}
func (s *AgentMutationService) Pipeline(ctx context.Context, id int64, version int, in PipelineInput) (domain.AgentMutationResult, error) {
	if err := s.pipeline.ValidateAgent(in); err != nil {
		return domain.AgentMutationResult{}, err
	}
	p := toDomain(in)
	p.ID = id
	p.Version = version
	kind := "create_pipeline"
	p.Status = domain.PipelineStatusDraft
	if id > 0 {
		if version < 1 {
			return domain.AgentMutationResult{}, ErrToolInput
		}
		current, err := s.pipeline.AgentMetadata(ctx, id)
		if err != nil {
			return domain.AgentMutationResult{}, err
		}
		if current.Version != version {
			return domain.AgentMutationResult{}, domain.ErrResourceConflict
		}
		p.Status = current.Status
		kind = "update_pipeline"
	}
	return s.execute(ctx, domain.AgentMutation{Kind: kind, Pipeline: p})
}
func (s *AgentMutationService) Status(ctx context.Context, id int64, version int, status string) (domain.AgentMutationResult, error) {
	if id < 1 || version < 1 || (status != domain.PipelineStatusEnabled && status != domain.PipelineStatusDisabled) {
		return domain.AgentMutationResult{}, ErrToolInput
	}
	return s.execute(ctx, domain.AgentMutation{Kind: "set_pipeline_status", Pipeline: &domain.Pipeline{ID: id, Version: version, Status: status}})
}
