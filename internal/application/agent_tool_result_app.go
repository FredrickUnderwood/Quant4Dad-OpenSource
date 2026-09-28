package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/service"
)

type AgentToolResultApplication struct {
	audit *service.AgentToolAuditService
	runs  *AgentRunApplication
}

func NewAgentToolResultApplication(audit *service.AgentToolAuditService, runs *AgentRunApplication) *AgentToolResultApplication {
	return &AgentToolResultApplication{audit: audit, runs: runs}
}
func (a *AgentToolResultApplication) Get(ctx context.Context, actor, run, call string) (service.AgentToolResult, error) {
	result, err := a.audit.Result(ctx, actor, run, call)
	if !errors.Is(err, service.ErrAgentToolNotFound) || a.runs == nil {
		return result, err
	}
	// Runtime guards reject before Gateway dispatch, so there is deliberately no
	// database audit row. Read only the authenticated journal of an owned Run;
	// neither browser-supplied arguments nor transcript text establish a result.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, err := a.runs.Get(ctx, actor, run)
	if err != nil {
		return result, err
	}
	if snapshot.LastEventID == "" || snapshot.LastEventID == "0" {
		return result, service.ErrAgentToolNotFound
	}
	stop := errors.New("tool_result_replay_complete")
	var name string
	started, finished, limited := false, false, false
	count, size := 0, 0
	_, err = a.runs.Events(ctx, actor, run, "0", agentbridge.StreamCallbacks{Event: func(event agentbridge.Event) error {
		count++
		body, encodeErr := sonic.Marshal(event)
		size += len(body)
		if encodeErr != nil || count > 8192 || size > 8<<20 {
			limited = true
			return stop
		}
		if event.RunID != run || event.SessionID != snapshot.SessionID {
			return nil
		}
		switch data := event.Data.(type) {
		case agentbridge.RunStartedData:
			started = event.Type == "run.started" && data.MessageID == snapshot.MessageID && data.ExecutionEnvelopeDigest == snapshot.ExecutionEnvelopeDigest
		case agentbridge.ToolProposedData:
			if data.ToolCallID == call {
				name = ""
			}
			if started && event.Type == "tool.proposed" && data.ToolCallID == call && data.ArgumentsOmitted != nil && *data.ArgumentsOmitted && len(data.Arguments) == 0 && data.IdempotencyKey == "q4d:"+run+":"+call {
				name = strings.TrimPrefix(data.Name, "mcp__q4d__")
			}
		case agentbridge.ToolData:
			if data.ToolCallID == call {
				name = ""
			}
		case agentbridge.ApprovalRequiredData:
			if data.ToolCallID == call {
				name = ""
			}
		case agentbridge.ToolFailedData:
			if event.Type == "tool.failed" && data.ToolCallID == call && name != "" && preDispatchFailure(data.Code) {
				result = service.AgentToolResult{RunID: run, ToolCallID: call, ToolName: name,
					Status: "failed", ErrorCode: data.Code, ExecutionStage: "pre_dispatch"}
				finished = true
				return stop
			}
		}
		// Stop at the captured journal boundary even if the Run is still active.
		// A missing call is not permission to wait for or invent a future call.
		if event.ID == snapshot.LastEventID {
			finished = true
			return stop
		}
		return nil
	}})
	if limited {
		return service.AgentToolResult{}, service.ErrAgentToolStore
	}
	if !finished && err != nil {
		return service.AgentToolResult{}, err
	}
	if result.ExecutionStage == "pre_dispatch" {
		return result, nil
	}
	return service.AgentToolResult{}, service.ErrAgentToolNotFound
}

func preDispatchFailure(code string) bool {
	switch code {
	case "agent_tool_forbidden", "agent_run_budget_exceeded", "agent_capability_expired",
		"agent_configuration_unavailable", "agent_tool_context_or_arguments_rejected",
		"agent_tool_cancelled", "agent_tool_rejected":
		return true
	default:
		return false
	}
}
