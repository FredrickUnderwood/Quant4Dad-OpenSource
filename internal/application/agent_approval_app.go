package application

import (
	"context"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
	"time"
)

type AgentApprovalApplication struct {
	approval *service.AgentApprovalService
	runs     *AgentRunApplication
	runtime  *service.AgentRunRuntimeService
}

func NewAgentApprovalApplication(approval *service.AgentApprovalService, runs *AgentRunApplication, runtime *service.AgentRunRuntimeService) *AgentApprovalApplication {
	return &AgentApprovalApplication{approval, runs, runtime}
}

type AgentApprovalDecisionResult struct {
	Approval  domain.AgentApprovalReceipt `json:"approval"`
	Delivered bool                        `json:"delivered"`
}

func (a *AgentApprovalApplication) Get(ctx context.Context, actor, id string) (domain.AgentApprovalReceipt, error) {
	row, err := a.approval.Get(ctx, actor, id)
	if err == nil && (row.Status == "pending" || row.Status == "approved") && !time.Now().Before(row.ExpiresAt) {
		row.Status = "expired"
	}
	return row, err
}
func (a *AgentApprovalApplication) Decide(ctx context.Context, actor, id, decision string) (AgentApprovalDecisionResult, error) {
	var result AgentApprovalDecisionResult
	if decision != "allow_once" && decision != "reject" {
		return result, service.ErrAgentApproval
	}
	row, err := a.approval.Get(ctx, actor, id)
	if err != nil {
		return result, err
	}
	result.Approval = row
	if row.Status == "consumed" && decision == "allow_once" {
		result.Delivered = true
		return result, nil
	}
	claims, err := a.runs.Authorization(ctx, row.RunID)
	if err != nil || claims.Envelope.ActorID != actor || claims.SessionID != row.SessionID || claims.EnvelopeDigest != row.EnvelopeDigest {
		return result, service.ErrAgentApproval
	}
	allowed, err := a.runs.ToolAuthority(ctx, claims)
	if err != nil || !allowed {
		return result, service.ErrAgentApproval
	}
	row, receipt, err := a.approval.Decide(ctx, actor, id, decision)
	if err != nil {
		return result, err
	}
	result.Approval = row
	err = a.runtime.DecideApproval(ctx, id, agentbridge.ApprovalDecision{RunID: row.RunID, ToolCallID: row.ToolCallID, Decision: decision, ApprovalReceipt: receipt})
	if err != nil {
		return result, err
	}
	result.Delivered = true
	return result, nil
}
