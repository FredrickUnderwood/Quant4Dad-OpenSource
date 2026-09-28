package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"path/filepath"
	"slices"

	"github.com/quant4dad/internal/agentrunauth"
)

type AgentRunsConfig struct {
	Enabled           bool                       `yaml:"enabled"`
	SigningKeyID      string                     `yaml:"signing_key_id"`
	SigningPrivateKey string                     `yaml:"signing_private_key" json:"-"`
	Manifest          AgentRunManifest           `yaml:"manifest"`
	ReleaseFile       string                     `yaml:"release_file"`       // Optional operator-owned active-generation.json, read on admission/policy checks.
	ReleasePublicKey  string                     `yaml:"release_public_key"` // Ed25519 public key, base64url; separate from Run signing keys.
	AllowEdge         bool                       `yaml:"allow_edge"`
	Profiles          map[string]AgentRunProfile `yaml:"profiles"`
}

func (AgentRunsConfig) String() string   { return "AgentRunsConfig{redacted}" }
func (AgentRunsConfig) GoString() string { return "AgentRunsConfig{redacted}" }

var ErrAgentRunsConfig = errors.New("agent_runs_configuration_invalid")

func (c *Config) ValidateAgentRuns() error {
	if c == nil || !c.Agent.Runs.Enabled || c.ValidateAgentSessions() != nil {
		return ErrAgentRunsConfig
	}
	r := c.Agent.Runs
	if (r.ReleaseFile == "") != (r.ReleasePublicKey == "") {
		return ErrAgentRunsConfig
	}
	if r.ReleasePublicKey != "" {
		key, err := base64.RawURLEncoding.Strict().DecodeString(r.ReleasePublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize || !filepath.IsAbs(r.ReleaseFile) || filepath.Clean(r.ReleaseFile) != r.ReleaseFile {
			return ErrAgentRunsConfig
		}
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(r.SigningPrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || base64.RawURLEncoding.EncodeToString(key) != r.SigningPrivateKey {
		return ErrAgentRunsConfig
	}
	if _, err = agentrunauth.NewSigner(r.SigningKeyID, c.Agent.Bootstrap.CapabilityIssuer, ed25519.PrivateKey(key)); err != nil {
		return ErrAgentRunsConfig
	}
	if c.Agent.Bootstrap.CapabilityPublicKeys[r.SigningKeyID] != base64.RawURLEncoding.EncodeToString(key[32:]) ||
		r.Manifest.DSHVersion != "0.1.2-alpha.5" || len(r.Profiles) < 1 || len(r.Profiles) > 128 {
		return ErrAgentRunsConfig
	}
	for id, p := range r.Profiles {
		e := r.Manifest.Envelope(p)
		if p.Budgets.MaxToolCalls > 0 {
			if !slices.Contains([]string{"research", "strategy_lab", "pipeline_builder"}, id) || !c.Agent.Gateway.Enabled {
				return ErrAgentRunsConfig
			}
			// Research uses the live server catalog, never a configured allowlist
			// or caller-selected revision. Validate the remaining envelope here.
			e.ToolCatalogRevision = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}
		e.RunID, e.ClientRequestID, e.ActorID = "validation", "validation", "local-user"
		e.ProductProfile, e.ProfileRevision = id, c.Agent.Sessions.Profiles[id]
		e.Provider, e.Model, e.ModelConfigRevision = "validation", "validation", "validation"
		if _, err = agentrunauth.EnvelopeDigest(e); err != nil {
			return ErrAgentRunsConfig
		}
	}
	return nil
}
func (c *Config) loadAgentRuns() error {
	if !c.Agent.Runs.Enabled {
		return nil
	}
	return c.ValidateAgentRuns()
}

func (c *Config) AgentRunSigner() (*agentrunauth.Signer, error) {
	if err := c.ValidateAgentRuns(); err != nil {
		return nil, err
	}
	key, _ := base64.RawURLEncoding.DecodeString(c.Agent.Runs.SigningPrivateKey)
	return agentrunauth.NewSigner(c.Agent.Runs.SigningKeyID, c.Agent.Bootstrap.CapabilityIssuer, ed25519.PrivateKey(key))
}

type AgentRunManifest struct {
	Q4DVersion          string `yaml:"q4d_version"`
	AgentImageDigest    string `yaml:"agent_image_digest"`
	AgentRuntimeVersion string `yaml:"agent_runtime_version"`
	AdapterVersion      string `yaml:"adapter_version"`
	DSHVersion          string `yaml:"dsh_version"`
}
type AgentRunProfile struct {
	PromptBundleDigest  string               `yaml:"prompt_bundle_digest"`
	SkillsDigest        string               `yaml:"skills_digest"`
	ToolCatalogRevision string               `yaml:"tool_catalog_revision"`
	Budgets             agentrunauth.Budgets `yaml:"budgets"`
}

func (m AgentRunManifest) Envelope(p AgentRunProfile) agentrunauth.Envelope {
	return agentrunauth.Envelope{Q4DVersion: m.Q4DVersion, AgentImageDigest: m.AgentImageDigest,
		AgentRuntimeVersion: m.AgentRuntimeVersion, AdapterVersion: m.AdapterVersion, DSHVersion: m.DSHVersion, BridgeProtocol: 1,
		PromptBundleDigest: p.PromptBundleDigest, SkillsDigest: p.SkillsDigest, ToolCatalogRevision: p.ToolCatalogRevision, Budgets: p.Budgets}
}
