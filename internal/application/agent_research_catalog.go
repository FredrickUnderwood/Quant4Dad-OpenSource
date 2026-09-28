package application

import (
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"slices"
)

// Bootstrap, grant issuance and Gateway share this startup catalog. Never load
// a discovery response or a browser allowlist into the execution policy.
func researchBootstrapCatalog(cfg *config.Config, catalogs []*ToolCatalogApplication) (*domain.AgentBootstrapCatalog, error) {
	return profileBootstrapCatalog(cfg, catalogs, "research")
}
func profileBootstrapCatalog(cfg *config.Config, catalogs []*ToolCatalogApplication, profile string) (*domain.AgentBootstrapCatalog, error) {
	if !cfg.Agent.Runs.Enabled || cfg.Agent.Runs.Profiles[profile].Budgets.MaxToolCalls == 0 {
		return nil, nil
	}
	if cfg.ValidateAgentGateway() != nil || len(catalogs) != 1 || catalogs[0] == nil {
		return nil, config.ErrAgentGatewayConfig
	}
	definitions := catalogs[0].Definitions(profile)
	names := P0ProfileTools(profile)
	if len(definitions) == 0 || len(names) == 0 {
		return nil, config.ErrAgentGatewayConfig
	}
	out := &domain.AgentBootstrapCatalog{Profile: profile, Revision: catalogs[0].Revision(profile), Tools: make([]domain.AgentBootstrapTool, 0, len(definitions))}
	for _, tool := range definitions {
		if !slices.Contains(names, tool.Name) || tool.Deprecated || (profile == "research" && tool.Risk != "R0") {
			return nil, config.ErrAgentGatewayConfig
		}
		out.Tools = append(out.Tools, domain.AgentBootstrapTool{Name: tool.Name, Description: tool.Description,
			InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema, Risk: tool.Risk, TimeoutMS: tool.TimeoutMS, MaxResult: tool.MaxResult})
	}
	return out, nil
}

func P0ProfileTools(profile string) []string {
	market := []string{"list_instruments", "get_instrument", "query_kline", "read_kline_file", "analyze_kline", "execute_python", "latest_bar_date", "get_data_coverage"}
	news := []string{"list_news", "get_news", "list_events", "get_event"}
	switch profile {
	case "research":
		return append(market, news...)
	case "strategy_lab":
		return append(market, "list_indicators", "list_strategies", "get_strategy", "validate_strategy", "create_strategy", "update_strategy", "list_cost_models", "run_backtest", "list_backtests", "get_backtest_job", "get_backtest_report")
	case "pipeline_builder":
		return append(news, "get_pipeline_node_types", "list_pipelines", "get_pipeline", "validate_pipeline", "create_pipeline", "update_pipeline", "dry_run_pipeline_safe", "set_pipeline_status")
	default:
		return nil
	}
}
