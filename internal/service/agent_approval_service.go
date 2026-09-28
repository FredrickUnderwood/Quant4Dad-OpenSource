package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

var ErrAgentApproval = errors.New("agent_approval_rejected")

type AgentApprovalService struct {
	repo   *repository.AgentApprovalRepository
	signer *agentrunauth.Signer
}

func NewAgentApprovalService(repo *repository.AgentApprovalRepository, signer *agentrunauth.Signer) *AgentApprovalService {
	return &AgentApprovalService{repo, signer}
}
func (s *AgentApprovalService) Ensure(ctx context.Context, audit domain.AgentToolAudit, deadline time.Time) (domain.AgentApprovalReceipt, error) {
	sum := sha256.Sum256([]byte("approval:" + audit.ID))
	expires := time.Now().UTC().Add(5 * time.Minute)
	if deadline.Before(expires) {
		expires = deadline.UTC()
	}
	candidate := domain.AgentApprovalReceipt{ID: hex.EncodeToString(sum[:]), AuditID: audit.ID, ActorID: audit.ActorID, SessionID: audit.SessionID, RunID: audit.RunID, ToolCallID: audit.ToolCallID, ToolName: audit.ToolName, ArgsHash: audit.ArgsHash, EnvelopeDigest: audit.EnvelopeDigest, Risk: audit.Risk, Status: "pending", ExpiresAt: expires}
	row, err := s.repo.Ensure(ctx, candidate)
	if err != nil || row.AuditID != candidate.AuditID || row.ActorID != candidate.ActorID || row.ArgsHash != candidate.ArgsHash || row.EnvelopeDigest != candidate.EnvelopeDigest || row.Risk != candidate.Risk || !time.Now().Before(row.ExpiresAt) {
		return row, ErrAgentApproval
	}
	return row, nil
}
func (s *AgentApprovalService) Get(ctx context.Context, actor, id string) (domain.AgentApprovalReceipt, error) {
	row, err := s.repo.Get(ctx, actor, id)
	if err != nil {
		return row, ErrAgentApproval
	}
	return row, nil
}
func (s *AgentApprovalService) Decide(ctx context.Context, actor, id, decision string) (domain.AgentApprovalReceipt, string, error) {
	if decision != "allow_once" && decision != "reject" {
		return domain.AgentApprovalReceipt{}, "", ErrAgentApproval
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.AgentApprovalReceipt{}, "", ErrAgentApproval
	}
	row, err := s.repo.Decide(ctx, actor, id, decision, hex.EncodeToString(random[:]))
	if err != nil {
		return row, "", ErrAgentApproval
	}
	if decision == "reject" {
		return row, "", nil
	}
	if row.ReceiptNonce == nil {
		return row, "", ErrAgentApproval
	}
	receipt, err := s.signer.ApprovalReceipt(row.ID, *row.ReceiptNonce)
	if err != nil {
		return row, "", ErrAgentApproval
	}
	return row, receipt, nil
}
func (s *AgentApprovalService) Verify(row domain.AgentApprovalReceipt, receipt string) error {
	if row.Status != "approved" || row.ReceiptNonce == nil || !time.Now().Before(row.ExpiresAt) {
		return ErrAgentApproval
	}
	expected, err := s.signer.ApprovalReceipt(row.ID, *row.ReceiptNonce)
	if err != nil || subtle.ConstantTimeCompare([]byte(receipt), []byte(expected)) != 1 {
		return ErrAgentApproval
	}
	return nil
}
