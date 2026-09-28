package service

import (
	"context"
	"sort"

	"github.com/quant4dad/internal/domain"
)

// GetAgentBootstrapModels derives eligible candidates and revision from ONE
// persisted snapshot. No last-good credential can override disable or revoke.
// Eligibility is configuration validity only, never proof of model readiness.
func (s *SettingService) GetAgentBootstrapModels(ctx context.Context) (string, []domain.AgentBootstrapProvider, error) {
	snapshot, err := s.GetLLMModelSnapshot(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(snapshot.Providers) > 128 {
		return "", nil, ErrModelConfigInvalid
	}
	providers := make([]domain.AgentBootstrapProvider, 0, len(snapshot.Providers))
	for name, p := range snapshot.Providers {
		if modelCandidate(name, p).Status != "unverified" {
			continue
		}
		// Use the same explicit-or-pinned limits advertised by the Go catalog.
		a, _ := effectiveAgentOptions(p)
		enabled := true
		a.Enabled, a.Protocol = &enabled, modelProtocol(p.Type)
		providers = append(providers, domain.AgentBootstrapProvider{ID: name, Type: p.Type, BaseURL: p.BaseURL,
			DefaultModel: p.DefaultModel, APIKey: p.APIKey, Agent: a})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return snapshot.Revision, providers, nil
}
