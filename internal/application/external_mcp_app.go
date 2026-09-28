package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	appLog "github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/tooljson"
	"go.uber.org/zap"
)

// ExternalMCPApplication shares the Agent's business catalog, while explicitly
// owning a separate token authority. It never issues or accepts Run capabilities
// and cannot weaken the internal Gateway's user-approval policy.
type ExternalMCPApplication struct {
	catalog         *ToolCatalogApplication
	audit           *service.AgentToolAuditService
	approval        *service.AgentApprovalService
	sessions        *service.ExternalMCPSessionService
	tokenAuthorized bool
	profiles        map[string]string
	names           []string
	revision        string
}

func NewExternalMCPApplication(catalog *ToolCatalogApplication, audit *service.AgentToolAuditService, approval *service.AgentApprovalService, sessions *service.ExternalMCPSessionService, tokenAuthorized bool) (*ExternalMCPApplication, error) {
	if catalog == nil || audit == nil || approval == nil || sessions == nil {
		return nil, service.ErrExternalMCPConfiguration
	}
	a := &ExternalMCPApplication{catalog: catalog, audit: audit, approval: approval, sessions: sessions, tokenAuthorized: tokenAuthorized, profiles: map[string]string{}}
	for _, profile := range []string{"research", "strategy_lab", "pipeline_builder"} {
		for _, definition := range catalog.Definitions(profile) {
			if !slices.Contains(P0ProfileTools(profile), definition.Name) {
				return nil, service.ErrExternalMCPConfiguration
			}
			if _, ok := a.profiles[definition.Name]; !ok {
				a.profiles[definition.Name] = profile
				a.names = append(a.names, definition.Name)
			}
		}
	}
	if len(a.names) == 0 {
		return nil, service.ErrExternalMCPConfiguration
	}
	sort.Strings(a.names)
	body, err := sonic.Config{SortMapKeys: true}.Froze().Marshal(a.Definitions())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	a.revision = "sha256:" + hex.EncodeToString(sum[:])
	return a, nil
}

func (a *ExternalMCPApplication) Definitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(a.names))
	for _, name := range a.names {
		for _, definition := range a.catalog.Definitions(a.profiles[name]) {
			if definition.Name == name {
				definitions = append(definitions, definition)
				break
			}
		}
	}
	return definitions
}

func (a *ExternalMCPApplication) Revision() string { return a.revision }

func (a *ExternalMCPApplication) CreateSession(ctx context.Context) (domain.ExternalMCPSession, error) {
	return a.sessions.Create(ctx, a.revision, a.names, a.tokenAuthorized)
}

func (a *ExternalMCPApplication) authorize(ctx context.Context, session string) (agentrunauth.Claims, domain.ExternalMCPSession, error) {
	return a.sessions.Authorize(ctx, session, a.revision, a.names, a.tokenAuthorized)
}

func (a *ExternalMCPApplication) Session(ctx context.Context, session string) (domain.ExternalMCPSession, error) {
	_, view, err := a.authorize(ctx, session)
	return view, err
}

func (a *ExternalMCPApplication) CloseSession(ctx context.Context, session string) error {
	return a.sessions.Revoke(ctx, session)
}

// Call binds a client RPC identity to one exact canonical argument set. Reusing
// an ID with changed arguments fails, and uncertain/pending work is not retried.
func (a *ExternalMCPApplication) Call(ctx context.Context, session string, rpcID []byte, name string, args []byte, started func() error) ([]byte, string, error) {
	claims, _, err := a.authorize(ctx, session)
	if err != nil {
		return nil, "", err
	}
	profile, ok := a.profiles[name]
	if !ok {
		return nil, "", ErrToolForbidden
	}
	call, key, err := externalMCPCallIdentity(claims.Envelope.RunID, rpcID)
	if err != nil {
		return nil, "", err
	}
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
	row, fresh, err := a.audit.Reserve(ctx, claims, call, key, name, hash, risk)
	if err != nil {
		return nil, "", err
	}
	if _, _, err = a.authorize(ctx, session); err != nil {
		if fresh {
			_ = a.audit.Finish(ctx, row, nil, "external_mcp_session_unavailable")
		}
		return nil, "", err
	}
	if !fresh && row.Status != "pending_approval" {
		body, code, err := a.audit.Replay(row)
		if err != nil {
			return nil, "", err
		}
		if row.Started && started != nil && started() != nil {
			return nil, "", service.ErrAgentToolPending
		}
		return body, code, nil
	}
	var approval domain.AgentApprovalReceipt
	if risk == "R2" || risk == "R3" {
		if row.Started {
			return nil, "", service.ErrAgentToolPending
		}
		// This is the dedicated MCP token policy, not a human approval. Both the
		// durable session and audit actor identify its source as external-mcp.
		if !a.tokenAuthorized {
			return nil, "external_mcp_token_authorization_required", nil
		}
		approval, err = a.approval.Ensure(ctx, row, time.UnixMilli(claims.DeadlineMillis()))
		if err != nil {
			return nil, "", err
		}
		if approval.Status == "rejected" {
			return nil, "agent_approval_denied", nil
		}
		var receipt string
		approval, receipt, err = a.approval.Decide(ctx, domain.ExternalMCPActor, approval.ID, "allow_once")
		if err != nil {
			return nil, "", err
		}
		if err = a.approval.Verify(approval, receipt); err != nil {
			return nil, "", err
		}
		appLog.Info(ctx, "external MCP token authorized mutation", zap.String("authorization_mode", "token_authorized"), zap.String("tool", name), zap.String("session_id", session), zap.String("run_id", row.RunID), zap.String("tool_call_id", call))
	}
	ctx = service.WithAgentExecution(ctx, row, approval)
	if _, _, err = a.authorize(ctx, session); err != nil {
		_ = a.audit.Finish(ctx, row, nil, "external_mcp_session_unavailable")
		return nil, "", err
	}
	if err = a.audit.MarkStarted(ctx, row); err != nil {
		return nil, "", err
	}
	if started != nil && started() != nil {
		_ = a.audit.Finish(ctx, row, nil, "external_mcp_start_unconfirmed")
		return nil, "", service.ErrAgentToolPending
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(claims.DeadlineMillis()))
	defer cancel()
	body, invokeErr := a.catalog.Invoke(ctx, profile, name, args)
	code := toolFailureCode(invokeErr)
	// A committed effect is retained even if the session is revoked in flight.
	if invokeErr == nil && (risk == "R2" || risk == "R3" || name == "run_backtest") {
		if err = a.audit.Finish(ctx, row, body, ""); err != nil {
			return nil, "", err
		}
		if _, _, err = a.authorize(ctx, session); err != nil {
			return nil, "", err
		}
		return body, "", nil
	}
	if _, _, err = a.authorize(ctx, session); err != nil {
		body, code = nil, "external_mcp_session_unavailable"
	}
	if err = a.audit.Finish(ctx, row, body, code); err != nil {
		return nil, "", err
	}
	return body, code, nil
}

func externalMCPCallIdentity(run string, raw []byte) (string, string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 1024 {
		return "", "", service.ErrToolInput
	}
	var canonical []byte
	if raw[0] == '"' {
		var value string
		if sonic.Unmarshal(raw, &value) != nil || len(value) > 128 {
			return "", "", service.ErrToolInput
		}
		canonical, _ = sonic.Marshal(value)
	} else {
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || n < -9007199254740991 || n > 9007199254740991 {
			return "", "", service.ErrToolInput
		}
		canonical = []byte(strconv.FormatInt(n, 10))
	}
	sum := sha256.Sum256(append([]byte("external-mcp-rpc:"), canonical...))
	// Encode 128 hash bits in the same bounded Crockford shape as Agent calls.
	n := new(big.Int).SetBytes(sum[:16])
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var encoded [26]byte
	mask := big.NewInt(31)
	for i := 25; i >= 0; i-- {
		encoded[i] = alphabet[new(big.Int).And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	call := string(encoded[:])
	key := "q4d:" + run + ":" + call
	if !service.AgentToolIdentity(run, call, key) {
		return "", "", service.ErrToolInput
	}
	return call, key, nil
}
