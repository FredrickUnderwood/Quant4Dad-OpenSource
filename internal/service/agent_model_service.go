package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

type LLMProvider = domain.LLMProvider
type AgentModelOptions = domain.AgentModelOptions

var (
	ErrModelInputInvalid  = errors.New("model_config_input_invalid")
	ErrModelConfigInvalid = errors.New("model_config_invalid")
	ErrModelNotFound      = errors.New("model_provider_not_found")
	ErrModelRevisionStale = errors.New("model_config_revision_stale")
)

var modelRevisionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,128}$`)
var modelProviderPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// LLMModelSnapshot is for trusted Go callers only, never an HTTP response.
type LLMModelSnapshot struct {
	Revision  string
	Providers map[string]LLMProvider
	probes    []byte
}

func (LLMModelSnapshot) String() string   { return "LLMModelSnapshot{redacted}" }
func (LLMModelSnapshot) GoString() string { return "LLMModelSnapshot{redacted}" }

type LLMProviderMasked struct {
	Type              string             `json:"type"`
	BaseURL           string             `json:"base_url"`
	DefaultModel      string             `json:"default_model"`
	HasAPIKey         bool               `json:"has_api_key"`
	Agent             *AgentModelOptions `json:"agent,omitempty"`
	CredentialRevoked bool               `json:"credential_revoked"`
}

type LLMProvidersView struct {
	Revision  string                       `json:"revision"`
	Providers map[string]LLMProviderMasked `json:"providers"`
}

type AgentModelCandidate struct {
	Provider        string                        `json:"provider"`
	Model           string                        `json:"model"`
	Protocol        string                        `json:"protocol"`
	ContextWindow   int                           `json:"context_window"`
	MaxOutputTokens int                           `json:"max_output_tokens"`
	LimitsSource    string                        `json:"limits_source"`
	Status          string                        `json:"status"`
	Reason          string                        `json:"reason"`
	Probe           *domain.AgentModelProbeResult `json:"probe,omitempty"`
}

type AgentModelCatalog struct {
	Revision string                `json:"revision"`
	Models   []AgentModelCandidate `json:"models"`
}

// Pointer fields distinguish omission (keep) from an explicit empty/zero value.
// HTTP rejects explicit null and unknown fields before constructing this patch.
type AgentModelOptionsPatch struct {
	Enabled         *bool   `json:"enabled,omitempty"`
	Protocol        *string `json:"protocol,omitempty"`
	ContextWindow   *int    `json:"context_window,omitempty"`
	MaxOutputTokens *int    `json:"max_output_tokens,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type ModelCredentialUpdate struct {
	Action string  `json:"action"`
	Value  *string `json:"value,omitempty"`
}

func (ModelCredentialUpdate) String() string   { return "ModelCredentialUpdate{redacted}" }
func (ModelCredentialUpdate) GoString() string { return "ModelCredentialUpdate{redacted}" }

type LLMProviderPatch struct {
	Type         *string                 `json:"type,omitempty"`
	BaseURL      *string                 `json:"base_url,omitempty"`
	DefaultModel *string                 `json:"default_model,omitempty"`
	Agent        *AgentModelOptionsPatch `json:"agent,omitempty"`
	APIKeyUpdate *ModelCredentialUpdate  `json:"api_key_update,omitempty"`
}

func (LLMProviderPatch) String() string   { return "LLMProviderPatch{redacted}" }
func (LLMProviderPatch) GoString() string { return "LLMProviderPatch{redacted}" }

func decodeModelSnapshot(raw repository.LLMProviderSnapshot) (LLMModelSnapshot, error) {
	out := LLMModelSnapshot{Providers: map[string]LLMProvider{}, probes: raw.Probes}
	if len(raw.Value) == 0 || sonic.Unmarshal(raw.Value, &out.Providers) != nil || out.Providers == nil {
		return LLMModelSnapshot{}, ErrModelConfigInvalid
	}
	if len(raw.Revision) != 0 {
		if sonic.Unmarshal(raw.Revision, &out.Revision) != nil || !modelRevisionPattern.MatchString(out.Revision) {
			return LLMModelSnapshot{}, ErrModelConfigInvalid
		}
	}
	return out, nil
}

// GetLLMModelSnapshot reads both values atomically. Legacy installations get one
// opaque revision on first access; credentials never participate in its value.
func (s *SettingService) GetLLMModelSnapshot(ctx context.Context) (LLMModelSnapshot, error) {
	raw, err := s.repo.GetLLMProviderSnapshot(ctx)
	if err != nil {
		return LLMModelSnapshot{}, err
	}
	out, err := decodeModelSnapshot(raw)
	if err != nil {
		return LLMModelSnapshot{}, err
	}
	if out.Revision != "" {
		return out, nil
	}
	return s.mutateModels(ctx, "", func(map[string]LLMProvider) error { return nil })
}

func (s *SettingService) GetLLMProviders(ctx context.Context) (map[string]LLMProvider, error) {
	snapshot, err := s.GetLLMModelSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.Providers, nil
}

func modelView(snapshot LLMModelSnapshot) LLMProvidersView {
	out := LLMProvidersView{Revision: snapshot.Revision, Providers: make(map[string]LLMProviderMasked, len(snapshot.Providers))}
	for name, p := range snapshot.Providers {
		// Legacy endpoints can contain userinfo or query credentials. Never expose
		// those through a browser response, even before the next configuration edit.
		endpoint := p.BaseURL
		if !validModelURL(endpoint) {
			endpoint = ""
		}
		out.Providers[name] = LLMProviderMasked{Type: p.Type, BaseURL: endpoint, DefaultModel: p.DefaultModel,
			HasAPIKey: p.APIKey != "", Agent: p.Agent, CredentialRevoked: p.CredentialRevoked}
	}
	return out
}

func (s *SettingService) GetLLMProvidersView(ctx context.Context) (LLMProvidersView, error) {
	snapshot, err := s.GetLLMModelSnapshot(ctx)
	if err != nil {
		return LLMProvidersView{}, err
	}
	return modelView(snapshot), nil
}

func (s *SettingService) GetLLMProvidersMasked(ctx context.Context) (map[string]LLMProviderMasked, error) {
	view, err := s.GetLLMProvidersView(ctx)
	return view.Providers, err
}

// mutateModels owns validation/merge and revision generation. The repository
// owns locking and persistence; no caller can observe a half-updated pair.
func (s *SettingService) mutateModels(ctx context.Context, expected string, mutate func(map[string]LLMProvider) error) (LLMModelSnapshot, error) {
	var result LLMModelSnapshot
	changed := false
	err := s.repo.UpdateLLMProviders(ctx, func(raw repository.LLMProviderSnapshot) (repository.LLMProviderSnapshot, error) {
		current, err := decodeModelSnapshot(raw)
		if err != nil {
			return raw, err
		}
		if expected != "" && expected != current.Revision {
			return raw, ErrModelRevisionStale
		}
		// A second decode isolates the original value for semantic no-op detection.
		before, err := decodeModelSnapshot(raw)
		if err != nil {
			return raw, err
		}
		if err := mutate(current.Providers); err != nil {
			return raw, err
		}
		changed = !reflect.DeepEqual(before.Providers, current.Providers)
		if changed || current.Revision == "" {
			var entropy [16]byte
			if _, err := rand.Read(entropy[:]); err != nil {
				return raw, err
			}
			current.Revision = hex.EncodeToString(entropy[:])
		}
		value, err := sonic.Marshal(current.Providers)
		if err != nil {
			return raw, err
		}
		revision, err := sonic.Marshal(current.Revision)
		if err != nil {
			return raw, err
		}
		result = current
		return repository.LLMProviderSnapshot{Value: value, Revision: revision}, nil
	})
	if err != nil {
		return LLMModelSnapshot{}, err
	}
	if changed {
		logger.L().Info("llm providers updated", zap.Int("count", len(result.Providers)), zap.String("revision", result.Revision))
	}
	return result, nil
}

// SetLLMProviders preserves the old full-map replacement API. Omitted providers
// are deleted; empty keys and omitted Agent metadata retain the stored values.
// Only a nonempty replacement key may clear the server-owned revoke marker.
func (s *SettingService) SetLLMProviders(ctx context.Context, providers map[string]LLMProvider) error {
	_, err := s.ReplaceLLMProviders(ctx, providers, "")
	return err
}

func (s *SettingService) ReplaceLLMProviders(ctx context.Context, providers map[string]LLMProvider, expected string) (LLMProvidersView, error) {
	if providers == nil || len(providers) > 128 {
		return LLMProvidersView{}, ErrModelInputInvalid
	}
	snapshot, err := s.mutateModels(ctx, expected, func(current map[string]LLMProvider) error {
		merged := make(map[string]LLMProvider, len(providers))
		for name, p := range providers {
			prev := current[name]
			p.CredentialRevoked = prev.CredentialRevoked
			if p.APIKey == "" {
				p.APIKey = prev.APIKey
			} else {
				p.CredentialRevoked = false
			}
			if p.Agent == nil {
				p.Agent = prev.Agent
			}
			if !validModelProvider(name, p) {
				return ErrModelInputInvalid
			}
			merged[name] = p
		}
		clear(current)
		for name, p := range merged {
			current[name] = p
		}
		return nil
	})
	if err != nil {
		return LLMProvidersView{}, err
	}
	return modelView(snapshot), nil
}

func (s *SettingService) PatchLLMProvider(ctx context.Context, name string, patch LLMProviderPatch, expected string) (LLMProvidersView, error) {
	snapshot, err := s.mutateModels(ctx, expected, func(current map[string]LLMProvider) error {
		p, exists := current[name]
		if !exists {
			return ErrModelNotFound
		}
		if patch.Type != nil {
			p.Type = *patch.Type
		}
		if patch.BaseURL != nil {
			p.BaseURL = *patch.BaseURL
		}
		if patch.DefaultModel != nil {
			p.DefaultModel = *patch.DefaultModel
		}
		if a := patch.Agent; a != nil {
			if p.Agent == nil {
				p.Agent = &AgentModelOptions{}
			}
			if a.Enabled != nil {
				p.Agent.Enabled = a.Enabled
			}
			if a.Protocol != nil {
				p.Agent.Protocol = *a.Protocol
			}
			if a.ContextWindow != nil {
				p.Agent.ContextWindow = *a.ContextWindow
			}
			if a.MaxOutputTokens != nil {
				p.Agent.MaxOutputTokens = *a.MaxOutputTokens
			}
			if a.ReasoningEffort != nil {
				p.Agent.ReasoningEffort = *a.ReasoningEffort
			}
		}
		if u := patch.APIKeyUpdate; u != nil {
			switch u.Action {
			case "keep":
				if u.Value != nil {
					return ErrModelInputInvalid
				}
			case "replace":
				if u.Value == nil || strings.TrimSpace(*u.Value) == "" {
					return ErrModelInputInvalid
				}
				p.APIKey, p.CredentialRevoked = *u.Value, false
			case "revoke":
				if u.Value != nil {
					return ErrModelInputInvalid
				}
				p.APIKey, p.CredentialRevoked = "", true
			default:
				return ErrModelInputInvalid
			}
		}
		if !validModelProvider(name, p) && !modelRevocationOnly(patch) {
			return ErrModelInputInvalid
		}
		current[name] = p
		return nil
	})
	if err != nil {
		return LLMProvidersView{}, err
	}
	return modelView(snapshot), nil
}

// Revocation and disable must remain possible even for an invalid legacy
// endpoint or metadata. They do not authorize unrelated configuration edits.
func modelRevocationOnly(patch LLMProviderPatch) bool {
	if patch.Type != nil || patch.BaseURL != nil || patch.DefaultModel != nil {
		return false
	}
	revoked := patch.APIKeyUpdate != nil && patch.APIKeyUpdate.Action == "revoke"
	if patch.APIKeyUpdate != nil && !revoked {
		return false
	}
	if a := patch.Agent; a != nil {
		if a.Enabled == nil || *a.Enabled || a.Protocol != nil || a.ContextWindow != nil || a.MaxOutputTokens != nil || a.ReasoningEffort != nil {
			return false
		}
		return true
	}
	return revoked
}

func (s *SettingService) DeleteLLMProvider(ctx context.Context, name, expected string) (LLMProvidersView, error) {
	snapshot, err := s.mutateModels(ctx, expected, func(current map[string]LLMProvider) error {
		if _, exists := current[name]; !exists {
			return ErrModelNotFound
		}
		delete(current, name)
		return nil
	})
	if err != nil {
		return LLMProvidersView{}, err
	}
	return modelView(snapshot), nil
}

func validModelText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

func validModelURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(s, "#") && validModelText(s, 2048)
}

func validModelProvider(name string, p LLMProvider) bool {
	if strings.TrimSpace(name) == "" || !validModelText(name, 128) || !validModelURL(p.BaseURL) ||
		!validModelText(p.DefaultModel, 128) || !validModelText(p.APIKey, 8192) || (p.Type != "openai" && p.Type != "anthropic") {
		return false
	}
	if a := p.Agent; a != nil {
		if a.ContextWindow < 0 || a.ContextWindow > 100000000 || a.MaxOutputTokens < 0 || a.MaxOutputTokens > 10000000 ||
			(a.ContextWindow > 0 && a.MaxOutputTokens >= a.ContextWindow) || !validModelText(a.ReasoningEffort, 32) {
			return false
		}
		if a.Protocol != "" && a.Protocol != modelProtocol(p.Type) {
			return false
		}
	}
	return true
}

func modelProtocol(kind string) string {
	switch kind {
	case "openai":
		return "openai-completions"
	case "anthropic":
		return "anthropic-messages"
	default:
		return ""
	}
}

// GetAgentModelCatalog merges current configuration with unexpired probe facts.
// Runtime independently gates Session creation on its own applied generation.
func (s *SettingService) GetAgentModelCatalog(ctx context.Context) (AgentModelCatalog, error) {
	snapshot, err := s.GetLLMModelSnapshot(ctx)
	if err != nil {
		return AgentModelCatalog{}, err
	}
	out := AgentModelCatalog{Revision: snapshot.Revision, Models: make([]AgentModelCandidate, 0, len(snapshot.Providers))}
	probes, err := decodeModelProbes(snapshot.probes)
	if err != nil {
		return AgentModelCatalog{}, err
	}
	for name, p := range snapshot.Providers {
		candidate := modelCandidate(name, p)
		for _, state := range probes {
			if candidate.Status != "unverified" || state.Provider != name || state.Revision != snapshot.Revision {
				continue
			}
			now := time.Now().UnixMilli()
			if state.Result == nil {
				if state.StartedAtMS <= now+5000 && now-state.StartedAtMS < 20000 {
					candidate.Status, candidate.Reason = "probing", "probe_in_progress"
				}
			} else if probe := state.Result; probe.Model == p.DefaultModel && probe.ContextWindow == candidate.ContextWindow && probe.MaxOutputTokens == candidate.MaxOutputTokens && probe.Valid(now) {
				candidate.Status, candidate.Reason, candidate.Probe = probe.Status, probe.Reason, probe
			}
		}
		out.Models = append(out.Models, candidate)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Provider < out.Models[j].Provider })
	return out, nil
}

func modelCandidate(name string, p LLMProvider) AgentModelCandidate {
	m := AgentModelCandidate{Provider: name, Model: p.DefaultModel, Protocol: modelProtocol(p.Type), Status: "unverified", Reason: "probe_required"}
	a, source := effectiveAgentOptions(p)
	m.ContextWindow, m.MaxOutputTokens, m.LimitsSource = a.ContextWindow, a.MaxOutputTokens, source
	switch {
	case p.CredentialRevoked:
		m.Status, m.Reason = "unavailable", "credential_revoked"
	case p.Agent != nil && p.Agent.Enabled != nil && !*p.Agent.Enabled:
		m.Status, m.Reason = "unavailable", "disabled"
	case !validModelProvider(name, p) || !modelProviderPattern.MatchString(name) || !modelIDPattern.MatchString(p.DefaultModel):
		m.Status, m.Reason = "incompatible_config", "invalid_provider_config"
	case m.ContextWindow == 0:
		m.Status, m.Reason = "incompatible_config", "context_window_required"
	case m.MaxOutputTokens == 0:
		m.Status, m.Reason = "incompatible_config", "max_output_tokens_required"
	case m.MaxOutputTokens >= m.ContextWindow:
		m.Status, m.Reason = "incompatible_config", "invalid_provider_config"
	}
	return m
}
