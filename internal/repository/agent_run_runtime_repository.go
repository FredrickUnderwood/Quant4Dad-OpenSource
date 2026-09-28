package repository

import (
	"context"
	"github.com/quant4dad/internal/agentbridge"
)

type AgentRunRuntimeRepository struct{ client *agentbridge.Client }

func NewAgentRunRuntimeRepository(client *agentbridge.Client) *AgentRunRuntimeRepository {
	return &AgentRunRuntimeRepository{client}
}
func (r *AgentRunRuntimeRepository) Prompt(ctx context.Context, input agentbridge.PromptRequest) (agentbridge.PromptAccepted, error) {
	return r.client.Prompt(ctx, input)
}
func (r *AgentRunRuntimeRepository) GetRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	return r.client.GetRun(ctx, session, id)
}
func (r *AgentRunRuntimeRepository) CancelRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	return r.client.CancelRun(ctx, session, id)
}
func (r *AgentRunRuntimeRepository) ReconcileRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	return r.client.ReconcileRun(ctx, session, id)
}

func (r *AgentRunRuntimeRepository) DecideApproval(ctx context.Context, id string, in agentbridge.ApprovalDecision) (agentbridge.ApprovalAcknowledged, error) {
	return r.client.DecideApproval(ctx, id, in)
}

func (r *AgentRunRuntimeRepository) RuntimeCapabilities(ctx context.Context) (agentbridge.Capabilities, error) {
	if err := r.client.Health(ctx); err != nil {
		return agentbridge.Capabilities{}, err
	}
	return r.client.Capabilities(ctx)
}
func (r *AgentRunRuntimeRepository) StreamEvents(ctx context.Context, session, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
	return r.client.StreamEvents(ctx, session, id, cursor, callbacks)
}
