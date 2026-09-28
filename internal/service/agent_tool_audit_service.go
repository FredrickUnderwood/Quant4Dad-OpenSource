package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"regexp"
	"time"

	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

var ErrAgentToolConflict = errors.New("agent_tool_call_conflict")
var ErrAgentToolPending = errors.New("agent_tool_result_unknown")
var ErrAgentToolStore = errors.New("agent_tool_storage_unavailable")
var ErrAgentToolBudget = errors.New("agent_tool_budget_exceeded")
var toolCallID = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

type AgentToolAuditService struct {
	repo      *repository.AgentToolAuditRepository
	artifacts *repository.AgentToolArtifactRepository
}

func NewAgentToolAuditService(repo *repository.AgentToolAuditRepository, artifacts *repository.AgentToolArtifactRepository) *AgentToolAuditService {
	return &AgentToolAuditService{repo, artifacts}
}
func AgentToolIdentity(run, call, key string) bool {
	return toolCallID.MatchString(call) && key == "q4d:"+run+":"+call && len(key) <= 64
}

func (s *AgentToolAuditService) ExistingRisk(ctx context.Context, run, call string) (string, error) {
	row, err := s.repo.GetCall(ctx, run, call)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", ErrAgentToolStore
	}
	return row.Risk, nil
}

func (s *AgentToolAuditService) Reserve(ctx context.Context, claims agentrunauth.Claims, call, key, name, hash string, risks ...string) (domain.AgentToolAudit, bool, error) {
	if !AgentToolIdentity(claims.Envelope.RunID, call, key) {
		return domain.AgentToolAudit{}, false, ErrToolInput
	}
	digest := sha256.Sum256([]byte(key))
	risk, status := "R0", "executing"
	if len(risks) > 0 {
		risk = risks[0]
	}
	if risk == "R2" || risk == "R3" {
		status = "pending_approval"
	} else if risk != "R0" && risk != "R1" {
		return domain.AgentToolAudit{}, false, ErrToolInput
	}
	candidate := domain.AgentToolAudit{ID: hex.EncodeToString(digest[:]), ActorID: claims.Envelope.ActorID, SessionID: claims.SessionID, RunID: claims.Envelope.RunID, ToolCallID: call, ToolName: name, IdempotencyKey: key, ArgsHash: hash, EnvelopeDigest: claims.EnvelopeDigest, Risk: risk, Status: status}
	row, created, err := s.repo.Reserve(ctx, candidate, claims.Envelope.Budgets.MaxToolCalls)
	if errors.Is(err, repository.ErrToolAuditBudget) {
		return row, false, ErrAgentToolBudget
	}
	if errors.Is(err, repository.ErrToolAuditDenied) {
		return row, false, ErrAgentRunRejected
	}
	if err != nil {
		return row, false, ErrAgentToolStore
	}
	if row.ID != candidate.ID || row.ActorID != candidate.ActorID || row.SessionID != candidate.SessionID || row.ToolName != name || row.IdempotencyKey != key || row.ArgsHash != hash || row.EnvelopeDigest != candidate.EnvelopeDigest || row.Risk != risk {
		return row, false, ErrAgentToolConflict
	}
	return row, created, nil
}
func (s *AgentToolAuditService) Replay(row domain.AgentToolAudit) ([]byte, string, error) {
	if row.Status == "pending_approval" {
		return nil, "", ErrAgentToolPending
	}
	if row.Status == "executing" {
		if row.Risk == "R2" || row.Risk == "R3" || row.ToolName == "run_backtest" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			effect, err := s.repo.Effect(ctx, row.ID)
			if err == nil && effect.ArgsHash == row.ArgsHash {
				body, err := sonic.Marshal(map[string]any{"data": sonicRaw(effect.ResultJSON), "untrusted_data": true})
				if err == nil && s.Finish(ctx, row, body, "") == nil {
					return body, "", nil
				}
				return nil, "", ErrAgentToolStore
			}
		}
		return nil, "", ErrAgentToolPending
	}
	if (row.Status == "failed" || row.Status == "expired") && row.ErrorCode != "" {
		return nil, row.ErrorCode, nil
	}
	if row.Status != "succeeded" || row.ResultRef == "" || !row.Started {
		return nil, "", ErrAgentToolStore
	}
	body, err := s.artifacts.Get(row.ResultRef)
	if err != nil {
		return nil, "", ErrAgentToolStore
	}
	return body, "", nil
}

type sonicRaw string

func (v sonicRaw) MarshalJSON() ([]byte, error) { return []byte(v), nil }
func (s *AgentToolAuditService) MarkStarted(ctx context.Context, row domain.AgentToolAudit) error {
	if s.repo.MarkStarted(ctx, row.ID) != nil {
		return ErrAgentToolStore
	}
	return nil
}
func (s *AgentToolAuditService) Finish(ctx context.Context, row domain.AgentToolAudit, body []byte, code string) error {
	status, ref := "failed", ""
	if code == "" {
		var err error
		ref, err = s.artifacts.Put(body)
		if err != nil {
			return ErrAgentToolStore
		}
		status = "succeeded"
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if s.repo.Finish(finish, row.ID, status, ref, code) != nil {
		return ErrAgentToolStore
	}
	return nil
}
