package agentbridge

import (
	"context"
	"net/http"
	"time"

	"github.com/quant4dad/internal/domain"
)

func (c *Client) ProbeModel(ctx context.Context, request domain.AgentModelProbeRequest) (domain.AgentModelProbeResult, error) {
	if !request.Valid() {
		return domain.AgentModelProbeResult{}, ErrInvalidRequest
	}
	var result domain.AgentModelProbeResult
	if err := c.json(ctx, http.MethodPost, "/models/probe", request, http.StatusOK, &result); err != nil {
		return result, err
	}
	if !result.Valid(time.Now().UnixMilli()) || result.Provider != request.Provider || result.Model != request.Model ||
		result.ModelConfigRevision != request.ModelConfigRevision || result.ProbeVersion != request.ProbeVersion {
		return domain.AgentModelProbeResult{}, ErrProtocol
	}
	return result, nil
}
