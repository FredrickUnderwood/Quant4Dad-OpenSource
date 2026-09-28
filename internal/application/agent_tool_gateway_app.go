package application

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/tooljson"
	"gorm.io/gorm"
)

type AgentToolGatewayApplication struct {
	catalog  *ToolCatalogApplication
	audit    *service.AgentToolAuditService
	verifier *agentrunauth.Verifier
	approval *service.AgentApprovalService
}

func NewAgentToolGatewayApplication(catalog *ToolCatalogApplication, audit *service.AgentToolAuditService, cfg *config.Config, current func(context.Context, agentrunauth.Claims) (bool, error), approvals ...*service.AgentApprovalService) (*AgentToolGatewayApplication, error) {
	if catalog == nil || audit == nil || current == nil || cfg.ValidateAgentBootstrap() != nil {
		return nil, agentrunauth.ErrConfiguration
	}
	keys := map[string]ed25519.PublicKey{}
	for id, value := range cfg.Agent.Bootstrap.CapabilityPublicKeys {
		key, _ := base64.RawURLEncoding.DecodeString(value)
		keys[id] = key
	}
	verifier, err := agentrunauth.NewVerifier(cfg.Agent.Bootstrap.CapabilityIssuer, agentrunauth.GatewayAudience, keys, current)
	if err != nil {
		return nil, err
	}
	var approval *service.AgentApprovalService
	if len(approvals) > 0 {
		approval = approvals[0]
	}
	return &AgentToolGatewayApplication{catalog, audit, verifier, approval}, nil
}
func (a *AgentToolGatewayApplication) Authorize(ctx context.Context, token string) (agentrunauth.Claims, error) {
	claims, err := a.verifier.VerifyCapability(ctx, token, time.Now())
	if err != nil {
		return claims, service.ErrAgentRunRejected
	}
	profile := claims.Envelope.ProductProfile
	if !slices.Contains([]string{"research", "strategy_lab", "pipeline_builder"}, profile) || claims.Envelope.ToolCatalogRevision != a.catalog.Revision(profile) {
		return claims, service.ErrAgentRunRejected
	}
	names := []string{}
	for _, d := range a.catalog.Definitions(profile) {
		names = append(names, d.Name)
	}
	for _, name := range claims.AllowedTools {
		if !slices.Contains(names, name) {
			return claims, service.ErrAgentRunRejected
		}
	}
	return claims, nil
}
func (a *AgentToolGatewayApplication) Definitions(claims agentrunauth.Claims) []ToolDefinition {
	out := []ToolDefinition{}
	for _, definition := range a.catalog.Definitions(claims.Envelope.ProductProfile) {
		if slices.Contains(claims.AllowedTools, definition.Name) {
			out = append(out, definition)
		}
	}
	return out
}

// Call rechecks authority immediately before both a replay and a new dispatch.
// started must durably deliver the Q4D handshake; failed delivery prevents querying.
func (a *AgentToolGatewayApplication) Call(ctx context.Context, token, call, key, name string, args []byte, started func() error, receipts ...string) ([]byte, string, error) {
	claims, err := a.Authorize(ctx, token)
	if err != nil {
		return nil, "", err
	}
	if !slices.Contains(claims.AllowedTools, name) {
		return nil, "", ErrToolForbidden
	}
	profile := claims.Envelope.ProductProfile
	args, err = a.catalog.ValidateInput(profile, name, args)
	if err != nil {
		return nil, "", err
	}
	hash, err := tooljson.Hash(args)
	if err != nil {
		return nil, "", service.ErrToolInput
	}
	risk, err := a.audit.ExistingRisk(ctx, claims.Envelope.RunID, call)
	if err != nil {
		return nil, "", err
	}
	if risk == "" {
		risk, err = a.catalog.Risk(ctx, profile, name, args)
		if err != nil {
			return nil, "", err
		}
	}
	if (risk == "R2" || risk == "R3") && a.approval == nil {
		return nil, "", ErrToolForbidden
	}
	row, fresh, err := a.audit.Reserve(ctx, claims, call, key, name, hash, risk)
	if err != nil {
		return nil, "", err
	}
	if _, err = a.Authorize(ctx, token); err != nil {
		if fresh {
			_ = a.audit.Finish(ctx, row, nil, "agent_capability_rejected")
		}
		return nil, "", err
	}
	if !fresh && row.Status != "pending_approval" {
		body, code, err := a.audit.Replay(row)
		if err != nil {
			return nil, "", err
		}
		if row.Started && (started == nil || started() != nil) {
			return nil, "", service.ErrAgentToolPending
		}
		return body, code, nil
	}
	if risk == "R2" || risk == "R3" {
		if row.Started {
			return nil, "", service.ErrAgentToolPending
		}
		approval, err := a.approval.Ensure(ctx, row, time.UnixMilli(claims.DeadlineMillis()))
		if err != nil {
			return nil, "", err
		}
		if approval.Status == "rejected" {
			_ = a.audit.Finish(ctx, row, nil, "agent_approval_denied")
			return nil, "agent_approval_denied", nil
		}
		receipt := ""
		if len(receipts) > 0 {
			receipt = receipts[0]
		}
		if receipt == "" {
			challenge, _ := sonic.Marshal(map[string]any{"error": map[string]any{"code": "agent_approval_required", "approval_id": approval.ID, "arguments_hash": approval.ArgsHash, "risk": approval.Risk, "expires_at": approval.ExpiresAt.UTC().Format(time.RFC3339Nano)}})
			return challenge, "agent_approval_required", nil
		}
		if err := a.approval.Verify(approval, receipt); err != nil {
			return nil, "", err
		}
		ctx = service.WithAgentExecution(ctx, row, approval)
	} else if len(receipts) > 0 && receipts[0] != "" {
		return nil, "", service.ErrAgentApproval
	}
	if risk == "R0" || risk == "R1" {
		ctx = service.WithAgentExecution(ctx, row, domain.AgentApprovalReceipt{})
	}
	if err = a.audit.MarkStarted(ctx, row); err != nil {
		return nil, "", err
	}
	if started == nil || started() != nil {
		_ = a.audit.Finish(ctx, row, nil, "agent_tool_start_unconfirmed")
		return nil, "", service.ErrAgentToolPending
	}
	deadline := time.UnixMilli(claims.DeadlineMillis())
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	body, invokeErr := a.catalog.Invoke(ctx, profile, name, args)
	code := toolFailureCode(invokeErr)
	// A committed write is a fact even if authority is revoked while returning
	// its result. Persist that outcome before refusing the response.
	if (risk == "R2" || risk == "R3" || name == "run_backtest") && invokeErr == nil {
		if err = a.audit.Finish(ctx, row, body, ""); err != nil {
			return nil, "", err
		}
		if _, err = a.Authorize(ctx, token); err != nil {
			return nil, "", err
		}
		return body, "", nil
	}
	if _, err = a.Authorize(ctx, token); err != nil {
		body, code = nil, "agent_capability_rejected"
	}
	if err = a.audit.Finish(ctx, row, body, code); err != nil {
		return nil, "", err
	}
	return body, code, nil
}
func toolFailureCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, service.ErrStrategyValidationRequired):
		return "strategy_validation_required"
	case errors.Is(err, service.ErrStrategyValidationMismatch):
		return "strategy_validation_mismatch"
	case errors.Is(err, service.ErrStrategyValidationExpired):
		return "strategy_validation_expired"
	case errors.Is(err, service.ErrToolInput):
		return "tool_invalid_arguments"
	case errors.Is(err, ErrToolTimeout):
		return "tool_timeout"
	case errors.Is(err, ErrToolResultTooLarge):
		return "tool_result_too_large"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "tool_not_found"
	case errors.Is(err, domain.ErrResourceConflict):
		return "resource_version_conflict"
	case errors.Is(err, service.ErrAgentApproval):
		return "agent_approval_rejected"
	default:
		return "tool_unavailable"
	}
}
