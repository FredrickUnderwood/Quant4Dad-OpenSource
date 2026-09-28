package application

import (
	"context"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

type AgentModelProbeApplication struct {
	settings *service.SettingService
	runtime  *service.AgentModelProbeService
}

func NewAgentModelProbeApplication(settings *service.SettingService, runtime *service.AgentModelProbeService) *AgentModelProbeApplication {
	return &AgentModelProbeApplication{settings: settings, runtime: runtime}
}
func (a *AgentModelProbeApplication) Probe(ctx context.Context, provider string, force bool) (domain.AgentModelProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	snapshot, err := a.settings.GetLLMModelSnapshot(ctx)
	if err != nil {
		return domain.AgentModelProbeResult{}, err
	}
	p, exists := snapshot.Providers[provider]
	if !exists {
		return domain.AgentModelProbeResult{}, service.ErrModelNotFound
	}
	// Eligibility uses the same export rules as bootstrap, never cached readiness.
	revision, eligible, err := a.settings.GetAgentBootstrapModels(ctx)
	if err != nil {
		return domain.AgentModelProbeResult{}, err
	}
	if revision != snapshot.Revision {
		return domain.AgentModelProbeResult{}, service.ErrModelRevisionStale
	}
	valid := false
	for _, model := range eligible {
		if model.ID == provider {
			valid = true
		}
	}
	if !valid {
		return domain.AgentModelProbeResult{}, service.ErrAgentProbeConfig
	}
	request := domain.AgentModelProbeRequest{Provider: provider, Model: p.DefaultModel, ModelConfigRevision: revision, ProbeVersion: domain.AgentModelProbeVersion, Force: force}
	attempt, err := a.settings.BeginAgentModelProbe(ctx, request)
	if err != nil {
		return domain.AgentModelProbeResult{}, err
	}
	saved := false
	defer func() {
		if !saved {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = a.settings.FinishAgentModelProbe(cleanup, request, attempt, nil)
		}
	}()
	result, err := a.runtime.Probe(ctx, request)
	if err != nil {
		return result, err
	}
	if result.Provider != provider || result.Model != p.DefaultModel || result.ModelConfigRevision != revision || result.ProbeVersion != domain.AgentModelProbeVersion {
		return domain.AgentModelProbeResult{}, service.ErrAgentProbeUnavailable
	}
	if err = a.settings.FinishAgentModelProbe(ctx, request, attempt, &result); err != nil {
		return domain.AgentModelProbeResult{}, err
	}
	saved = true
	return result, nil
}
