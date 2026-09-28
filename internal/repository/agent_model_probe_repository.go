package repository

import (
	"context"

	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
)

type AgentModelProbeRepository struct{ client *agentbridge.Client }

func NewAgentModelProbeRepository(client *agentbridge.Client) *AgentModelProbeRepository {
	return &AgentModelProbeRepository{client: client}
}
func (r *AgentModelProbeRepository) ProbeModel(ctx context.Context, request domain.AgentModelProbeRequest) (domain.AgentModelProbeResult, error) {
	return r.client.ProbeModel(ctx, request)
}
