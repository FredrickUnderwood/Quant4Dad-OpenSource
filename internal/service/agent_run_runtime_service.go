package service

import (
	"context"
	"errors"
	"github.com/quant4dad/internal/agentbridge"
)

type AgentRunRuntimePort interface {
	Prompt(context.Context, agentbridge.PromptRequest) (agentbridge.PromptAccepted, error)
	GetRun(context.Context, string, string) (agentbridge.Run, error)
	CancelRun(context.Context, string, string) (agentbridge.Run, error)
	StreamEvents(context.Context, string, string, string, agentbridge.StreamCallbacks) (agentbridge.StreamResult, error)
}
type AgentRunRuntimeService struct{ repo AgentRunRuntimePort }

func (s *AgentRunRuntimeService) Reconcile(ctx context.Context, session, id string) (agentbridge.Run, error) {
	port, ok := s.repo.(interface {
		ReconcileRun(context.Context, string, string) (agentbridge.Run, error)
	})
	if !ok {
		return agentbridge.Run{}, ErrAgentRunUnavailable
	}
	v, err := port.ReconcileRun(ctx, session, id)
	return v, runRuntimeError(err)
}

func NewAgentRunRuntimeService(repo AgentRunRuntimePort) *AgentRunRuntimeService {
	return &AgentRunRuntimeService{repo}
}

func (s *AgentRunRuntimeService) DecideApproval(ctx context.Context, id string, in agentbridge.ApprovalDecision) error {
	port, ok := s.repo.(interface {
		DecideApproval(context.Context, string, agentbridge.ApprovalDecision) (agentbridge.ApprovalAcknowledged, error)
	})
	if !ok {
		return ErrAgentRunUnavailable
	}
	_, err := port.DecideApproval(ctx, id, in)
	return runRuntimeError(err)
}

func (s *AgentRunRuntimeService) Capabilities(ctx context.Context) (agentbridge.Capabilities, error) {
	port, ok := s.repo.(interface {
		RuntimeCapabilities(context.Context) (agentbridge.Capabilities, error)
	})
	if !ok {
		return agentbridge.Capabilities{}, ErrAgentRunUnavailable
	}
	value, err := port.RuntimeCapabilities(ctx)
	return value, runRuntimeError(err)
}
func runRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var bridge *agentbridge.Error
	if errors.As(err, &bridge) {
		switch bridge.Code {
		case "agent_run_not_found":
			if bridge.Status == 404 {
				return ErrAgentRunNotFound
			}
		case "agent_capability_rejected":
			return ErrAgentRunRejected
		case "agent_capability_expired":
			return ErrAgentRunExpired
		case "agent_request_conflict":
			return ErrAgentRunConflict
		case "agent_run_in_progress":
			return ErrAgentRunBusy
		case "agent_event_cursor_expired":
			return ErrAgentRunCursorExpired
		case "agent_invalid_event_cursor":
			return ErrAgentRunCursorInvalid
		}
	}
	return ErrAgentRunUnavailable
}

var ErrAgentRunCursorExpired = errors.New("agent_event_cursor_expired")
var ErrAgentRunCursorInvalid = errors.New("agent_invalid_event_cursor")

func (s *AgentRunRuntimeService) Events(ctx context.Context, session, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
	if agentbridge.ValidateEventCursor(cursor) != nil {
		return agentbridge.StreamResult{}, ErrAgentRunCursorInvalid
	}
	v, err := s.repo.StreamEvents(ctx, session, id, cursor, callbacks)
	return v, runRuntimeError(err)
}
func (s *AgentRunRuntimeService) Prompt(ctx context.Context, input agentbridge.PromptRequest) (agentbridge.PromptAccepted, error) {
	v, err := s.repo.Prompt(ctx, input)
	return v, runRuntimeError(err)
}
func (s *AgentRunRuntimeService) Get(ctx context.Context, session, id string) (agentbridge.Run, error) {
	v, err := s.repo.GetRun(ctx, session, id)
	return v, runRuntimeError(err)
}
func (s *AgentRunRuntimeService) Cancel(ctx context.Context, session, id string) (agentbridge.Run, error) {
	v, err := s.repo.CancelRun(ctx, session, id)
	return v, runRuntimeError(err)
}
