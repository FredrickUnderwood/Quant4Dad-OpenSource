package application

import (
	"context"
	"errors"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/service"
)

func TestPipelineDefinitionValidationMatchesWriteAndPreviewInputs(t *testing.T) {
	pl := service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))
	catalog := NewToolCatalogApplication()
	for _, definition := range CatalogValidationToolDefinitions(nil, pl, nil, nil) {
		catalog.Register(definition)
	}
	// Only schemas are used for mutations; no storage or approval is available.
	for _, definition := range CatalogWriteToolDefinitions(&service.AgentMutationService{}, pl) {
		catalog.Register(definition)
	}
	catalog.Register(SafePipelineToolDefinition(pl))
	pipeline := map[string]any{"name": "schema-fixture", "nodes": []any{map[string]any{
		"node_key": "filter", "type": "keyword_filter", "config": map[string]any{
			"keywords": []string{"fixture"}, "list_type": "whitelist"}}}, "edges": []any{}, "sources": []string{}}
	for _, status := range []string{"", "draft", "disabled", "enabled"} {
		if status == "" {
			delete(pipeline, "status")
		} else {
			pipeline["status"] = status
		}
		for _, name := range []string{"validate_pipeline", "create_pipeline", "update_pipeline", "dry_run_pipeline_safe"} {
			args := map[string]any{"pipeline": pipeline}
			if name == "update_pipeline" {
				args["id"], args["expected_version"] = 1, 1
			}
			if name == "dry_run_pipeline_safe" {
				args["sample_event"] = map[string]any{"text": "fixture"}
			}
			body, err := sonic.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			_, err = catalog.ValidateInput("pipeline_builder", name, body)
			if status != "" {
				if !errors.Is(err, service.ErrToolInput) {
					t.Fatalf("%s accepted status=%s although writes cannot accept lifecycle state: %v", name, status, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s rejected a shared definition: %v", name, err)
			}
			if name == "validate_pipeline" {
				result, err := catalog.Invoke(context.Background(), "pipeline_builder", name, body)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Data struct {
						Valid bool `json:"valid"`
					} `json:"data"`
				}
				if sonic.Unmarshal(result, &got) != nil || !got.Data.Valid {
					t.Fatalf("definition did not validate: %s", result)
				}
			}
		}
	}
}
