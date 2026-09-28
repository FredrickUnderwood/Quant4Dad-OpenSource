package service

import (
	"context"
	"github.com/quant4dad/config"
)

type AgentReleaseSource interface {
	Current(context.Context) (config.AgentRunManifest, error)
}
type AgentReleaseService struct{ source AgentReleaseSource }

func NewAgentReleaseService(source AgentReleaseSource) *AgentReleaseService {
	return &AgentReleaseService{source: source}
}
func (s *AgentReleaseService) Current(ctx context.Context) (config.AgentRunManifest, error) {
	return s.source.Current(ctx)
}
