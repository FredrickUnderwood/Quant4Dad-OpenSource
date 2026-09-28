package service

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	ErrAgentRunInput       = errors.New("agent_request_invalid")
	ErrAgentRunNotFound    = errors.New("agent_run_not_found")
	ErrAgentRunConflict    = errors.New("agent_request_conflict")
	ErrAgentRunUnavailable = errors.New("agent_run_runtime_unavailable")
	ErrAgentRunPending     = errors.New("agent_run_delivery_unconfirmed")
	ErrAgentRunRejected    = errors.New("agent_capability_rejected")
	ErrAgentRunExpired     = errors.New("agent_capability_expired")
	ErrAgentRunStore       = errors.New("agent_request_storage_invalid")
	ErrAgentRunBusy        = errors.New("agent_run_in_progress")
)
var runClientPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type AgentRunMessage struct {
	ClientRequestID string                    `json:"client_request_id"`
	Content         []agentbridge.TextContent `json:"content"`
}
type AgentRunService struct {
	repo   *repository.AgentRunRepository
	signer *agentrunauth.Signer
	keyID  string
}

func NewAgentRunService(repo *repository.AgentRunRepository, signer *agentrunauth.Signer, keyID string) *AgentRunService {
	return &AgentRunService{repo, signer, keyID}
}
func (s *AgentRunService) Scan(ctx context.Context, after string) ([]domain.AgentRequestBinding, error) {
	if after != "" && !sessionIDPattern.MatchString(after) {
		return nil, ErrAgentRunInput
	}
	rows, err := s.repo.Scan(ctx, after)
	if err != nil {
		return nil, ErrAgentRunStore
	}
	return rows, nil
}
func AgentRunHash(session string, input AgentRunMessage) (string, error) {
	if !sessionIDPattern.MatchString(session) || !runClientPattern.MatchString(input.ClientRequestID) || len(input.Content) != 1 || input.Content[0].Type != "text" {
		return "", ErrAgentRunInput
	}
	hash, err := agentrunauth.PromptHash(session, input.Content[0].Text)
	if err != nil {
		return "", ErrAgentRunInput
	}
	return hash, nil
}
func runStoreResult(row domain.AgentRequestBinding, err error) (domain.AgentRequestBinding, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentRunNotFound
	}
	if err != nil {
		return row, ErrAgentRunStore
	}
	if _, err = AgentRunClaims(row); err != nil {
		return row, err
	}
	return row, nil
}
func AgentRunClaims(row domain.AgentRequestBinding) (agentrunauth.Claims, error) {
	c, err := agentrunauth.ReadStoredClaims(row.ClaimsJSON)
	if err != nil || c.SessionID != row.SessionID || c.Envelope.RunID != row.ID || c.Envelope.ActorID != row.ActorID ||
		sessionDigest(c.Envelope.ClientRequestID) != row.ClientRequestKey || c.RequestHash != row.RequestHash || c.EnvelopeDigest != row.EnvelopeDigest || c.DeadlineMillis() != row.DeadlineMS {
		return agentrunauth.Claims{}, ErrAgentRunStore
	}
	return c, nil
}
func (s *AgentRunService) Find(ctx context.Context, actor, session string, input AgentRunMessage) (domain.AgentRequestBinding, error) {
	hash, err := AgentRunHash(session, input)
	if err != nil || !sessionActorPattern.MatchString(actor) {
		return domain.AgentRequestBinding{}, ErrAgentRunInput
	}
	row, err := s.repo.GetByKey(ctx, actor, session, sessionDigest(input.ClientRequestID))
	row, err = runStoreResult(row, err)
	if err == nil && row.RequestHash != hash {
		return row, ErrAgentRunConflict
	}
	return row, err
}
func (s *AgentRunService) Get(ctx context.Context, actor, id string) (domain.AgentRequestBinding, error) {
	row, err := s.PolicyRecord(ctx, id)
	if err == nil && row.ActorID != actor {
		return domain.AgentRequestBinding{}, ErrAgentRunNotFound
	}
	return row, err
}
func (s *AgentRunService) PolicyRecord(ctx context.Context, id string) (domain.AgentRequestBinding, error) {
	if !sessionIDPattern.MatchString(id) {
		return domain.AgentRequestBinding{}, ErrAgentRunInput
	}
	row, err := s.repo.Get(ctx, id)
	return runStoreResult(row, err)
}
func (s *AgentRunService) Prepare(ctx context.Context, session domain.AgentSessionBinding, input AgentRunMessage, envelope agentrunauth.Envelope, allowedTools ...string) (domain.AgentRequestBinding, error) {
	hash, err := AgentRunHash(session.ID, input)
	if err != nil {
		return domain.AgentRequestBinding{}, err
	}
	id, err := sessionID()
	if err != nil {
		return domain.AgentRequestBinding{}, ErrAgentRunStore
	}
	envelope.RunID, envelope.ClientRequestID, envelope.ActorID = id, input.ClientRequestID, session.ActorID
	_, claims, err := s.signer.Issue(agentrunauth.IssueRequest{SessionID: session.ID, RequestHash: hash, Envelope: envelope, AllowedTools: append([]string{}, allowedTools...)}, time.Now())
	if err != nil {
		return domain.AgentRequestBinding{}, ErrAgentRunRejected
	}
	data, err := sonic.Marshal(claims)
	if err != nil {
		return domain.AgentRequestBinding{}, ErrAgentRunStore
	}
	row, err := s.repo.InsertOrGet(ctx, domain.AgentRequestBinding{ID: id, SessionID: session.ID, ActorID: session.ActorID, ClientRequestKey: sessionDigest(input.ClientRequestID), RequestHash: hash, EnvelopeDigest: claims.EnvelopeDigest, ClaimsJSON: string(data), SigningKeyID: s.keyID, DeadlineMS: claims.DeadlineMillis()})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentSessionState
	}
	row, err = runStoreResult(row, err)
	if err == nil && row.RequestHash != hash {
		return row, ErrAgentRunConflict
	}
	if err == nil {
		logger.L().Info("agent request prepared", zap.String("run_id", row.ID), zap.String("session_id", row.SessionID))
	}
	return row, err
}
func (s *AgentRunService) Token(row domain.AgentRequestBinding) (string, error) {
	claims, err := AgentRunClaims(row)
	if err != nil {
		return "", err
	}
	if row.Revoked || row.SigningKeyID != s.keyID {
		return "", ErrAgentRunRejected
	}
	token, err := s.signer.SignStored(claims, time.Now())
	if errors.Is(err, agentrunauth.ErrExpired) {
		return "", ErrAgentRunExpired
	}
	if err != nil {
		return "", ErrAgentRunRejected
	}
	return token, nil
}
func (s *AgentRunService) Acknowledge(ctx context.Context, row domain.AgentRequestBinding, messageID string) error {
	if !runClientPattern.MatchString(messageID) || (row.MessageID != nil && *row.MessageID != messageID) {
		return ErrAgentRunStore
	}
	if row.MessageID != nil {
		return nil
	}
	if err := s.repo.Acknowledge(ctx, row.ID, messageID); err != nil {
		return ErrAgentRunStore
	}
	stored, err := s.Get(ctx, row.ActorID, row.ID)
	if err != nil || stored.MessageID == nil || *stored.MessageID != messageID {
		return ErrAgentRunStore
	}
	logger.L().Info("agent request durably accepted", zap.String("run_id", row.ID))
	return nil
}
func (s *AgentRunService) Revoke(ctx context.Context, row domain.AgentRequestBinding) error {
	if err := s.repo.Revoke(ctx, row.ActorID, row.ID); err != nil {
		return ErrAgentRunStore
	}
	logger.L().Info("agent run authorization revoked", zap.String("run_id", row.ID))
	return nil
}
