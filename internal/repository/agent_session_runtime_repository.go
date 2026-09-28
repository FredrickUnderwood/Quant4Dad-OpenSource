package repository

import (
	"context"
	"github.com/quant4dad/internal/agentbridge"
)

type AgentSessionRuntimeRepository struct{ client *agentbridge.Client }

func NewAgentSessionRuntimeRepository(client *agentbridge.Client) *AgentSessionRuntimeRepository {
	return &AgentSessionRuntimeRepository{client: client}
}
func (r *AgentSessionRuntimeRepository) CreateSession(ctx context.Context, request agentbridge.SessionCreate) (agentbridge.SessionCreated, error) {
	return r.client.CreateSession(ctx, request)
}
func (r *AgentSessionRuntimeRepository) Transcript(ctx context.Context, id string, query agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
	return r.client.Transcript(ctx, id, query)
}
