package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/bytedance/sonic"
	"maps"
	"regexp"
	"slices"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

const AgentBootstrapProtocol = "q4d-bootstrap-v1"

var bootstrapRevisionPattern = regexp.MustCompile(`^[0-9a-f]{32}\.[0-9a-f]{32}$`)

type AgentBootstrapApplication struct {
	settings     *service.SettingService
	epoch        string
	mcp          domain.AgentBootstrapMCP
	capability   domain.AgentBootstrapCapability
	toolCatalog  *domain.AgentBootstrapCatalog
	toolCatalogs []domain.AgentBootstrapCatalog
	profiles     []domain.AgentBootstrapProfile
}

func (AgentBootstrapApplication) String() string   { return "AgentBootstrapApplication{redacted}" }
func (AgentBootstrapApplication) GoString() string { return "AgentBootstrapApplication{redacted}" }

func NewAgentBootstrapApplication(settings *service.SettingService, cfg *config.Config, catalogs ...*ToolCatalogApplication) (*AgentBootstrapApplication, error) {
	if settings == nil || cfg.ValidateAgentBootstrap() != nil {
		return nil, config.ErrAgentBootstrapConfig
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, err
	}
	b := cfg.Agent.Bootstrap
	toolCatalog, err := researchBootstrapCatalog(cfg, catalogs)
	if err != nil {
		return nil, err
	}
	var all []domain.AgentBootstrapCatalog
	for _, profile := range []string{"pipeline_builder", "research", "strategy_lab"} {
		catalog, err := profileBootstrapCatalog(cfg, catalogs, profile)
		if err != nil {
			return nil, err
		}
		if catalog != nil {
			all = append(all, *catalog)
		}
	}
	var profiles []domain.AgentBootstrapProfile
	if cfg.Agent.Sessions.Enabled && cfg.Agent.Runs.Enabled {
		for _, id := range slices.Sorted(maps.Keys(cfg.Agent.Runs.Profiles)) {
			p := cfg.Agent.Runs.Profiles[id]
			catalogRevision := p.ToolCatalogRevision
			for _, catalog := range all {
				if catalog.Profile == id {
					catalogRevision = catalog.Revision
				}
			}
			profiles = append(profiles, domain.AgentBootstrapProfile{ID: id, Revision: cfg.Agent.Sessions.Profiles[id],
				PromptBundleDigest: p.PromptBundleDigest, SkillsDigest: p.SkillsDigest, ToolCatalogRevision: catalogRevision})
		}
	}
	return &AgentBootstrapApplication{settings: settings, epoch: hex.EncodeToString(epoch[:]), profiles: profiles,
		toolCatalog:  toolCatalog,
		toolCatalogs: all,
		mcp:          domain.AgentBootstrapMCP{URL: b.MCPURL, RuntimeToken: b.MCPToken, ConnectTimeoutMS: 5000, ToolTimeoutMS: 30000},
		capability: domain.AgentBootstrapCapability{Issuer: b.CapabilityIssuer, Algorithm: "Ed25519",
			TokenType: agentrunauth.TokenType, PublicKeys: maps.Clone(b.CapabilityPublicKeys)},
	}, nil
}

// Read always consults durable model state, including on conditional requests.
// A new application instance changes the opaque epoch: environment/key changes
// cannot be hidden by an unchanged model revision. No secret-derived hash is sent.
func (a *AgentBootstrapApplication) Read(ctx context.Context, known string) (domain.AgentBootstrap, bool, error) {
	if known != "" && !bootstrapRevisionPattern.MatchString(known) {
		return domain.AgentBootstrap{}, false, service.ErrModelInputInvalid
	}
	revision, providers, err := a.settings.GetAgentBootstrapModels(ctx)
	if err != nil {
		return domain.AgentBootstrap{}, false, err
	}
	out := domain.AgentBootstrap{ProtocolVersion: AgentBootstrapProtocol, Revision: a.epoch + "." + revision,
		ModelConfigRevision: revision}
	if known == out.Revision {
		// No secret-bearing response object on the 304 path.
		return out, true, nil
	}
	out.Providers, out.MCP, out.Capability = providers, a.mcp, a.capability
	out.Profiles = slices.Clone(a.profiles)
	out.Capability.PublicKeys = maps.Clone(a.capability.PublicKeys)
	if a.toolCatalog != nil {
		// Return detached metadata; callers cannot mutate the next snapshot.
		copy := *a.toolCatalog
		copy.Tools = make([]domain.AgentBootstrapTool, 0, len(a.toolCatalog.Tools))
		for _, tool := range a.toolCatalog.Tools {
			definition := cloneDefinition(ToolDefinition{InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema})
			tool.InputSchema, tool.OutputSchema = definition.InputSchema, definition.OutputSchema
			copy.Tools = append(copy.Tools, tool)
		}
		out.ToolCatalog = &copy
	}
	if len(a.toolCatalogs) > 0 {
		raw, _ := sonic.Marshal(a.toolCatalogs)
		if sonic.Unmarshal(raw, &out.ToolCatalogs) != nil {
			return domain.AgentBootstrap{}, false, config.ErrAgentBootstrapConfig
		}
	}
	return out, false, nil
}
