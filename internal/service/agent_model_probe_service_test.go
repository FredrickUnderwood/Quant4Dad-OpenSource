package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func probeFixture(t *testing.T, s *SettingService) (domain.AgentModelProbeRequest, domain.AgentModelProbeResult) {
	t.Helper()
	ctx := context.Background()
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": modelFixture()}); err != nil {
		t.Fatal(err)
	}
	snap := requireModelSnapshot(t, s)
	now := time.Now().UnixMilli()
	req := domain.AgentModelProbeRequest{Provider: "fixture", Model: "fixture-model", ModelConfigRevision: snap.Revision, ProbeVersion: domain.AgentModelProbeVersion}
	result := domain.AgentModelProbeResult{Provider: req.Provider, Model: req.Model, ModelConfigRevision: req.ModelConfigRevision, ProbeVersion: req.ProbeVersion, Status: "ready", Reason: "probe_passed", CheckedAtMS: now, ExpiresAtMS: now + 86400000, ContextWindow: 8192, MaxOutputTokens: 512, ContextSource: "explicit-config", ModelTurns: 2, ToolCalls: 1, FirstEventMS: 1}
	return req, result
}
func TestAgentModelProbePersistedCatalog(t *testing.T) {
	s, db := modelService(t)
	req, result := probeFixture(t, s)
	ctx := context.Background()
	attempt, err := s.BeginAgentModelProbe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Status != "probing" {
		t.Fatal("missing probing state", err)
	}
	if err = s.FinishAgentModelProbe(ctx, req, attempt, &result); err != nil {
		t.Fatal(err)
	}
	fresh := NewSettingService(repository.NewSettingRepository(db))
	catalog, err = fresh.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Status != "ready" || catalog.Revision != req.ModelConfigRevision {
		t.Fatal("probe did not survive service restart", err)
	}
	var row domain.Setting
	if err = db.First(&row, "`key` = ?", domain.SettingKeyAgentModelProbes).Error; err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{modelFixture().APIKey, modelFixture().BaseURL, "Q4D_ECHO", "Q4D_PROBE"} {
		if strings.Contains(string(row.Value), forbidden) {
			t.Fatal("probe cache contains sensitive content")
		}
	}
}
func TestAgentModelProbeRejectsLateRevision(t *testing.T) {
	s, _ := modelService(t)
	req, result := probeFixture(t, s)
	ctx := context.Background()
	attempt, err := s.BeginAgentModelProbe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "revoke"}}, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAgentModelProbe(ctx, req, attempt, &result); !errors.Is(err, ErrModelRevisionStale) {
		t.Fatal("late completion restored readiness", err)
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Reason != "credential_revoked" || catalog.Models[0].Probe != nil {
		t.Fatal("revocation was obscured", err)
	}
}
func TestAgentModelProbeAttemptOrdering(t *testing.T) {
	s, _ := modelService(t)
	req, result := probeFixture(t, s)
	ctx := context.Background()
	first, err := s.BeginAgentModelProbe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginAgentModelProbe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	failed := result
	failed.Status = "incompatible"
	failed.Reason = "probe_contract_mismatch"
	failed.ModelTurns = 1
	failed.ToolCalls = 0
	if err = s.FinishAgentModelProbe(ctx, req, second, &failed); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*domain.AgentModelProbeResult{&result, nil} {
		if err = s.FinishAgentModelProbe(ctx, req, first, value); !errors.Is(err, ErrModelRevisionStale) {
			t.Fatal("superseded attempt accepted", err)
		}
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Status != "incompatible" {
		t.Fatal("new outcome overwritten", err)
	}
}
func TestAgentModelProbeExpiryVersionAndRollback(t *testing.T) {
	s, db := modelService(t)
	req, result := probeFixture(t, s)
	ctx := context.Background()
	attempt, err := s.BeginAgentModelProbe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("CREATE TRIGGER reject_probe BEFORE UPDATE ON setting WHEN OLD.`key` = 'agent.model.probes' BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAgentModelProbe(ctx, req, attempt, &result); err == nil {
		t.Fatal("failed write accepted")
	}
	if err = db.Exec("DROP TRIGGER reject_probe").Error; err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAgentModelProbe(ctx, req, attempt, &result); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"expired", "old-version"} {
		row := result
		if mode == "expired" {
			row.CheckedAtMS -= 86400001
			row.ExpiresAtMS -= 86400001
		} else {
			row.ProbeVersion = "echo-old"
		}
		data, _ := sonic.Marshal([]modelProbeState{{Provider: req.Provider, Revision: req.ModelConfigRevision, Attempt: attempt, StartedAtMS: time.Now().UnixMilli(), Result: &row}})
		if err = db.Model(&domain.Setting{}).Where("`key` = ?", domain.SettingKeyAgentModelProbes).Update("value", data).Error; err != nil {
			t.Fatal(err)
		}
		catalog, err := s.GetAgentModelCatalog(ctx)
		if err != nil || catalog.Models[0].Status != "unverified" {
			t.Fatal("expired/versioned cache did not invalidate", mode, err)
		}
	}
}
