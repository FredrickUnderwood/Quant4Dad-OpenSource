package application

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/quant4dad/internal/indicator"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/service"
)

func TestStrategyToolsShareSelectedIndicatorParameterContract(t *testing.T) {
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogValidationToolDefinitions(service.NewStrategyService(nil), nil, nil, service.NewIndicatorService()) {
		catalog.Register(d)
	}
	for _, d := range CatalogWriteToolDefinitions(&service.AgentMutationService{}, service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))) {
		catalog.Register(d)
	}
	var shared map[string]any
	found := 0
	for _, d := range catalog.Definitions("strategy_lab") {
		if d.Name != "validate_strategy" && d.Name != "create_strategy" && d.Name != "update_strategy" {
			continue
		}
		found++
		var node any = d.InputSchema
		for _, key := range []string{"properties", "strategy", "properties", "body", "config", "properties", "indicators", "items", "properties", "params"} {
			m, ok := node.(map[string]any)
			if !ok {
				t.Fatalf("%s schema missing %s", d.Name, key)
			}
			if key == "config" {
				node = m["oneOf"].([]any)[0]
			} else {
				node = m[key]
			}
		}
		params := node.(map[string]any)
		description, _ := params["description"].(string)
		if !strings.Contains(description, "parameter_schema.properties for the selected indicator type") || !strings.Contains(description, "defaults come from that same schema") || strings.Contains(description, "source defaults to close") {
			t.Fatalf("%s ambiguous parameter scope: %q", d.Name, description)
		}
		if shared == nil {
			shared = params
		} else if !reflect.DeepEqual(shared, params) {
			t.Fatalf("%s diverges from the shared parameter contract", d.Name)
		}
	}
	if found != 3 {
		t.Fatalf("found %d strategy tools", found)
	}

	// Discovery and actual validation agree: source is not a universal parameter.
	sourceTypes := map[string]bool{"BOLL": true, "EMA": true, "MA": true, "MACD": true, "RSI": true}
	for _, row := range service.NewIndicatorService().List() {
		name := row["name"].(string)
		schema := row["parameter_schema"].(map[string]any)
		properties := schema["properties"].(map[string]any)
		source, present := properties["source"]
		if present != sourceTypes[name] {
			t.Fatalf("%s unexpected source support: %v", name, present)
		}
		if present && source.(map[string]any)["default"] != "close" {
			t.Fatalf("%s source default missing", name)
		}
		params := map[string]any{}
		for field, definition := range indicator.Parameters(name) {
			if definition.Required {
				params[field] = definition.Minimum
			}
		}
		if err := indicator.ValidateParams(name, params); err != nil {
			t.Fatalf("%s valid params rejected: %v", name, err)
		}
		params["source"] = "close"
		if err := indicator.ValidateParams(name, params); (err == nil) != present {
			t.Fatalf("%s discovery/validation disagree: %v", name, err)
		}
	}
}

func TestAgentIndicatorListCountAndOriginalDefinitions(t *testing.T) {
	svc := service.NewIndicatorService()
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogValidationToolDefinitions(nil, nil, nil, svc) {
		catalog.Register(d)
	}
	body, err := catalog.Invoke(context.Background(), "strategy_lab", "list_indicators", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data struct {
			Count int              `json:"count"`
			Items []map[string]any `json:"items"`
		} `json:"data"`
		Untrusted bool `json:"untrusted_data"`
	}
	if err := sonic.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	original, err := sonic.Marshal(svc.List())
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	if err := sonic.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}
	if !got.Untrusted || got.Data.Count != 7 || got.Data.Count != len(got.Data.Items) || !reflect.DeepEqual(got.Data.Items, want) {
		t.Fatalf("indicator count/definitions changed: %s", body)
	}
	for _, input := range [][]map[string]any{nil, {}} {
		empty, err := sonic.Marshal(indicatorListData(input))
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Count int              `json:"count"`
			Items []map[string]any `json:"items"`
		}
		if err := sonic.Unmarshal(empty, &value); err != nil {
			t.Fatal(err)
		}
		if value.Count != 0 || value.Items == nil || len(value.Items) != 0 {
			t.Fatalf("empty list must be count=0/items=[]: %s", empty)
		}
	}
}
