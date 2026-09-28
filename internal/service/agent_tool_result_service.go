package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

var ErrAgentToolNotFound = errors.New("agent_tool_not_found")

// AgentToolResult is a product projection. It never exposes storage references,
// capability identities, approval receipts or raw audit rows.
type AgentToolResult struct {
	RunID          string          `json:"run_id"`
	ToolCallID     string          `json:"tool_call_id"`
	ToolName       string          `json:"tool_name"`
	Status         string          `json:"status"`
	Risk           string          `json:"risk,omitempty"`
	ExecutionStage string          `json:"execution_stage,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
}

func (s *AgentToolAuditService) Result(ctx context.Context, actor, run, call string) (AgentToolResult, error) {
	var result AgentToolResult
	if !sessionIDPattern.MatchString(run) || !toolCallID.MatchString(call) || !sessionActorPattern.MatchString(actor) {
		return result, ErrAgentRunInput
	}
	row, err := s.repo.GetCall(ctx, run, call)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, ErrAgentToolNotFound
	}
	if err == nil && row.ActorID != actor {
		return result, ErrAgentRunNotFound
	}
	if err != nil {
		return result, ErrAgentToolStore
	}
	result = AgentToolResult{RunID: row.RunID, ToolCallID: row.ToolCallID, ToolName: row.ToolName, Status: row.Status, Risk: row.Risk, ErrorCode: row.ErrorCode}
	// Reading a card never executes a Tool or repairs a ledger entry. Background
	// maintenance publishes committed outcomes that were interrupted on return.
	if row.Status == "succeeded" {
		if !row.Started || row.ResultRef == "" {
			return AgentToolResult{}, ErrAgentToolStore
		}
		body, err := s.artifacts.Get(row.ResultRef)
		if err != nil || !sonic.Valid(body) {
			return AgentToolResult{}, ErrAgentToolStore
		}
		result.Result = body
	}
	return result, nil
}
