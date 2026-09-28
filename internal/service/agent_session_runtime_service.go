package service

import (
	"context"
	"errors"

	"github.com/quant4dad/internal/agentbridge"
)

type AgentSessionRuntimeBackend interface {
	CreateSession(context.Context, agentbridge.SessionCreate) (agentbridge.SessionCreated, error)
	Transcript(context.Context, string, agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error)
}
type AgentSessionRuntimeService struct{ backend AgentSessionRuntimeBackend }

func NewAgentSessionRuntimeService(backend AgentSessionRuntimeBackend) *AgentSessionRuntimeService {
	return &AgentSessionRuntimeService{backend: backend}
}
func (s *AgentSessionRuntimeService) Provision(ctx context.Context, request agentbridge.SessionCreate) (agentbridge.SessionCreated, string) {
	if s == nil || s.backend == nil {
		return agentbridge.SessionCreated{}, "agent_session_runtime_unavailable"
	}
	value, err := s.backend.CreateSession(ctx, request)
	if err == nil {
		return value, ""
	}
	var wire *agentbridge.Error
	if errors.As(err, &wire) {
		switch wire.Code {
		case "agent_session_unbound", "agent_session_storage_missing", "agent_session_conflict", "agent_configuration_conflict":
			return agentbridge.SessionCreated{}, wire.Code
		}
	}
	return agentbridge.SessionCreated{}, "agent_session_runtime_unavailable"
}
func (s *AgentSessionRuntimeService) Transcript(ctx context.Context, id string, query agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
	if s == nil || s.backend == nil {
		return agentbridge.TranscriptPage{}, ErrAgentSessionUnavailable
	}
	page, err := s.backend.Transcript(ctx, id, query)
	if err != nil {
		if errors.Is(err, agentbridge.ErrInvalidRequest) {
			return page, ErrAgentSessionInput
		}
		return agentbridge.TranscriptPage{}, ErrAgentSessionUnavailable
	}
	return page, nil
}
