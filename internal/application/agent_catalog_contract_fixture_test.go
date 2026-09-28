//go:build agentcatalogintegration

package application

import (
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/service"
)

// Export actual Go schemas for the pinned DSH integration tests. Synthetic
// services are never invoked; no database, credentials or network are needed.
func TestAgentCatalogContractFixture(t *testing.T) {
	pl := service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))
	catalog := NewAgentP0Catalog(&service.InstrumentService{}, service.NewIndicatorService(), service.NewStrategyService(nil), pl, &service.EventQueryService{}, &service.AgentCatalogQueryService{}, &service.AgentMutationService{}, &service.CostService{}, service.NewAgentKlineFileService(nil, catalogPythonExecutor{}))
	if len(catalog.Definitions("strategy_lab")) != len(P0ProfileTools("strategy_lab")) {
		t.Fatal("strategy catalog is incomplete")
	}
	body, err := sonic.Marshal(map[string]any{"profile": "strategy_lab", "revision": catalog.Revision("strategy_lab"), "tools": catalog.Definitions("strategy_lab")})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Q4D_CATALOG\t" + string(body))
}
