package service

import (
	"context"
	"time"
)

func (s *AgentToolAuditService) Maintain(ctx context.Context, now time.Time) (int64, int, error) {
	rows, err := s.repo.CommittedEffects(ctx)
	if err != nil {
		return 0, 0, ErrAgentToolStore
	}
	for _, row := range rows {
		if _, _, err := s.Replay(row); err != nil {
			return 0, 0, err
		}
	}
	changed, err := s.repo.Maintain(ctx, now, now.Add(-30*24*time.Hour))
	if err != nil {
		return 0, 0, ErrAgentToolStore
	}
	removed, err := s.artifacts.Sweep(ctx, now, s.repo.ArtifactReferenced)
	if err != nil {
		return changed, removed, ErrAgentToolStore
	}
	return changed, removed, nil
}
