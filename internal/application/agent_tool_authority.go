package application

import (
	"bytes"
	"context"

	"github.com/quant4dad/internal/agentrunauth"
)

// ToolAuthority adds live Runtime state to the original/current Go policy check.
// Prompt admission cannot use this check: the Run does not exist there yet.
func (a *AgentRunApplication) ToolAuthority(ctx context.Context, claims agentrunauth.Claims) (bool, error) {
	current, err := a.Authorization(ctx, claims.Envelope.RunID)
	if err != nil {
		return false, err
	}
	left, err := agentrunauth.CanonicalClaims(current)
	if err != nil {
		return false, err
	}
	right, err := agentrunauth.CanonicalClaims(claims)
	if err != nil || !bytes.Equal(left, right) {
		return false, err
	}
	run, err := a.runtime.Get(ctx, claims.SessionID, claims.Envelope.RunID)
	if err != nil {
		return false, err
	}
	row, err := a.runs.PolicyRecord(ctx, claims.Envelope.RunID)
	if err != nil || row.Revoked || run.MessageID == "" || (row.MessageID != nil && *row.MessageID != run.MessageID) {
		return false, err
	}
	return run.Durable && !run.Terminal && (run.State == "running" || run.State == "waiting_approval") && run.SessionID == claims.SessionID && run.RunID == claims.Envelope.RunID && run.ExecutionEnvelopeDigest == claims.EnvelopeDigest, nil
}
