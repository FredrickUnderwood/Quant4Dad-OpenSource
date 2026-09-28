package application

import "github.com/quant4dad/internal/service"

// Nil dependencies implement availability: a tool without its actual business
// adapter is not advertised or included in a Profile's capability digest.
func NewAgentP0Catalog(market *service.InstrumentService, indicators *service.IndicatorService, strategy *service.StrategyService, pipeline *service.PipelineService, events *service.EventQueryService, queries *service.AgentCatalogQueryService, mutations *service.AgentMutationService, cost *service.CostService, fileServices ...*service.AgentKlineFileService) *ToolCatalogApplication {
	catalog := NewToolCatalogApplication()
	marketTools := MarketToolDefinitions(market)
	if len(fileServices) > 0 && fileServices[0] != nil && market != nil {
		filtered := []ToolDefinition{}
		for _, tool := range marketTools {
			if tool.Name != "query_kline" {
				filtered = append(filtered, tool)
			}
		}
		marketTools = append(filtered, AgentKlineFileToolDefinitions(market, fileServices[0])...)
	}
	for _, group := range [][]ToolDefinition{
		marketTools, PipelineReadToolDefinitions(pipeline), EventToolDefinitions(events), CatalogQueryToolDefinitions(queries),
		CatalogValidationToolDefinitions(strategy, pipeline, market, indicators), CatalogWriteToolDefinitions(mutations, pipeline),
	} {
		for _, tool := range group {
			catalog.Register(tool)
		}
	}
	if pipeline != nil {
		catalog.Register(SafePipelineToolDefinition(pipeline))
	}
	if mutations != nil && cost != nil {
		catalog.Register(AgentBacktestToolDefinition(mutations, cost))
	}
	return catalog
}

type ToolAvailability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func (a *ToolCatalogApplication) Availability(profile string) []ToolAvailability {
	result := []ToolAvailability{}
	for _, name := range P0ProfileTools(profile) {
		item := ToolAvailability{Name: name}
		for _, tool := range a.Definitions(profile) {
			if tool.Name == name {
				item.Available = true
				break
			}
		}
		if !item.Available {
			item.Reason = "business_adapter_unavailable"
		}
		result = append(result, item)
	}
	return result
}
