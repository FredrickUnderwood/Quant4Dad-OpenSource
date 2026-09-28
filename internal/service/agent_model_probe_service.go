package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"go.uber.org/zap"
)

var ErrAgentProbeUnavailable = errors.New("agent_model_probe_unavailable")
var ErrAgentProbeConfig = errors.New("agent_model_config_incomplete")

// Each attempt has a random identity so an older HTTP completion cannot replace
// a newer forced probe, even when both used the same model revision.
type modelProbeState struct {
	Provider    string                        `json:"provider"`
	Revision    string                        `json:"revision"`
	Attempt     string                        `json:"attempt"`
	StartedAtMS int64                         `json:"started_at_ms"`
	Result      *domain.AgentModelProbeResult `json:"result,omitempty"`
}

func decodeModelProbes(data []byte) ([]modelProbeState, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var rows []modelProbeState
	if len(data) > 1<<20 || sonic.Unmarshal(data, &rows) != nil || rows == nil || len(rows) > 128 {
		return nil, ErrModelConfigInvalid
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if !modelProviderPattern.MatchString(r.Provider) || !modelRevisionPattern.MatchString(r.Revision) || !modelRevisionPattern.MatchString(r.Attempt) || r.StartedAtMS < 1 || seen[r.Provider] {
			return nil, ErrModelConfigInvalid
		}
		seen[r.Provider] = true
		if r.Result != nil && r.Result.ProbeVersion != domain.AgentModelProbeVersion {
			continue
		}
		if r.Result != nil && (!r.Result.Valid(r.Result.CheckedAtMS) || r.Result.Provider != r.Provider || r.Result.ModelConfigRevision != r.Revision) {
			return nil, ErrModelConfigInvalid
		}
	}
	return rows, nil
}
func (s *SettingService) BeginAgentModelProbe(ctx context.Context, request domain.AgentModelProbeRequest) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	attempt := hex.EncodeToString(entropy[:])
	err := s.repo.UpdateLLMProviders(ctx, func(raw repository.LLMProviderSnapshot) (repository.LLMProviderSnapshot, error) {
		snapshot, err := decodeModelSnapshot(raw)
		if err != nil {
			return raw, err
		}
		p, exists := snapshot.Providers[request.Provider]
		if !exists || snapshot.Revision != request.ModelConfigRevision || p.DefaultModel != request.Model {
			return raw, ErrModelRevisionStale
		}
		if modelCandidate(request.Provider, p).Status != "unverified" {
			return raw, ErrAgentProbeConfig
		}
		rows, err := decodeModelProbes(raw.Probes)
		if err != nil {
			return raw, err
		}
		kept := []modelProbeState{}
		for _, r := range rows {
			if r.Provider != request.Provider && r.Revision == snapshot.Revision {
				kept = append(kept, r)
			}
		}
		kept = append(kept, modelProbeState{Provider: request.Provider, Revision: snapshot.Revision, Attempt: attempt, StartedAtMS: time.Now().UnixMilli()})
		raw.Probes, err = sonic.Marshal(kept)
		return raw, err
	})
	return attempt, err
}

// nil result clears only this failed attempt. A superseded attempt cannot clear
// another caller's result. Model writes and probe completion use one DB lock.
func (s *SettingService) FinishAgentModelProbe(ctx context.Context, request domain.AgentModelProbeRequest, attempt string, result *domain.AgentModelProbeResult) error {
	if result != nil && (!result.Valid(time.Now().UnixMilli()) || result.Provider != request.Provider || result.Model != request.Model || result.ModelConfigRevision != request.ModelConfigRevision) {
		return ErrAgentProbeUnavailable
	}
	err := s.repo.UpdateLLMProviders(ctx, func(raw repository.LLMProviderSnapshot) (repository.LLMProviderSnapshot, error) {
		snapshot, err := decodeModelSnapshot(raw)
		if err != nil {
			return raw, err
		}
		p, exists := snapshot.Providers[request.Provider]
		candidate := modelCandidate(request.Provider, p)
		if !exists || snapshot.Revision != request.ModelConfigRevision || p.DefaultModel != request.Model || candidate.Status != "unverified" {
			return raw, ErrModelRevisionStale
		}
		if result != nil && (result.ContextWindow != candidate.ContextWindow || result.MaxOutputTokens != candidate.MaxOutputTokens) {
			return raw, ErrAgentProbeUnavailable
		}
		rows, err := decodeModelProbes(raw.Probes)
		if err != nil {
			return raw, err
		}
		kept := []modelProbeState{}
		found := false
		for _, row := range rows {
			if row.Provider != request.Provider {
				kept = append(kept, row)
				continue
			}
			if row.Attempt != attempt {
				return raw, ErrModelRevisionStale
			}
			found = true
			if result != nil {
				row.Result = result
				kept = append(kept, row)
			}
		}
		if !found {
			return raw, ErrModelRevisionStale
		}
		raw.Probes, err = sonic.Marshal(kept)
		return raw, err
	})
	if err == nil && result != nil {
		logger.L().Info("agent model probe recorded", zap.String("provider", result.Provider), zap.String("status", result.Status))
	}
	return err
}

// Infrastructure clients implement this port; application orchestration stays
// above model settings and the runtime service.
type AgentModelProbeBackend interface {
	ProbeModel(context.Context, domain.AgentModelProbeRequest) (domain.AgentModelProbeResult, error)
}
type AgentModelProbeService struct{ backend AgentModelProbeBackend }

func NewAgentModelProbeService(backend AgentModelProbeBackend) *AgentModelProbeService {
	return &AgentModelProbeService{backend: backend}
}
func (s *AgentModelProbeService) Probe(ctx context.Context, request domain.AgentModelProbeRequest) (domain.AgentModelProbeResult, error) {
	if s == nil || s.backend == nil {
		return domain.AgentModelProbeResult{}, ErrAgentProbeUnavailable
	}
	result, err := s.backend.ProbeModel(ctx, request)
	if err != nil {
		return domain.AgentModelProbeResult{}, ErrAgentProbeUnavailable
	}
	return result, nil
}
