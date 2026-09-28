package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

// Exercise independently authored synthetic fixtures against the validation
// catalog. No live Agent, production manifest or market-data access is required.
func TestAgentScriptStandaloneValidationFixtures(t *testing.T) {
	checkAgentScriptFixtures(t, "testdata/strategy-validation.json")
}

func checkAgentScriptFixtures(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		FixturePrefix string `json:"fixture_prefix"`
		Cases         []struct {
			ID         string `json:"id"`
			Assertions []struct {
				Strategy        map[string]any `json:"strategy"`
				BehaviorTests   []string       `json:"behavior_tests"`
				Report          map[string]any `json:"report"`
				Receipt         *bool          `json:"receipt"`
				Valid           bool           `json:"valid"`
				SchemaRejected  bool           `json:"schema_rejected"`
				ErrorPath       string         `json:"error_path"`
				ErrorContains   string         `json:"error_contains"`
				Checks          []string       `json:"checks"`
				WarningsContain []string       `json:"warnings_contain"`
			} `json:"validation_assertions"`
		} `json:"cases"`
	}
	if err := sonic.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewToolCatalogApplication()
	for _, definition := range CatalogValidationToolDefinitions(service.NewStrategyService(nil, files), nil, nil, nil) {
		catalog.Register(definition)
	}
	fixtures := 0
	for _, c := range manifest.Cases {
		for i, expected := range c.Assertions {
			if expected.Strategy == nil {
				t.Fatalf("fixture %s/%d has no strategy", c.ID, i+1)
			}
			fixtures++
			t.Run(c.ID+"/"+strconv.Itoa(i+1), func(t *testing.T) {
				args, err := sonic.Marshal(map[string]any{"strategy": expected.Strategy, "behavior_tests": append([]string{}, expected.BehaviorTests...)})
				if err != nil {
					t.Fatal(err)
				}
				args = []byte(strings.ReplaceAll(string(args), "{{fixture_prefix}}", manifest.FixturePrefix))
				ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "validation", ToolName: "validate_strategy", ActorID: "fixture", SessionID: "fixture"}, domain.AgentApprovalReceipt{})
				out, err := catalog.Invoke(ctx, "strategy_lab", "validate_strategy", args)
				if expected.SchemaRejected {
					if !errors.Is(err, service.ErrToolInput) {
						t.Fatalf("expected schema rejection; got %s, %v", out, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					Data struct {
						Valid    bool                          `json:"valid"`
						Errors   []service.ToolValidationIssue `json:"errors"`
						Checks   []string                      `json:"checks"`
						Warnings []string                      `json:"warnings"`
					} `json:"data"`
				}
				if err := sonic.Unmarshal(out, &result); err != nil {
					t.Fatal(err)
				}
				var decoded map[string]any
				if err := sonic.Unmarshal(out, &decoded); err != nil {
					t.Fatal(err)
				}
				if !matchesToolSchema(catalog.tools["validate_strategy"].OutputSchema, decoded) {
					t.Fatalf("output violates catalog schema: %s", out)
				}
				fields := decoded["data"].(map[string]any)
				if expected.Report != nil && !containsValidationFixtureFields(fields["script_report"], expected.Report) {
					t.Fatalf("report mismatch: %s", out)
				}
				if expected.Receipt != nil {
					id, _ := fields["validation_id"].(string)
					if *expected.Receipt != (len(id) == 64) {
						t.Fatalf("receipt mismatch: %s", out)
					}
				}
				data := result.Data
				if data.Valid != expected.Valid {
					t.Fatalf("expected valid=%v; got %s", expected.Valid, out)
				}
				if expected.Valid && (data.Errors == nil || len(data.Errors) != 0) {
					t.Fatalf("valid result must contain errors=[]; got %s", out)
				}
				if expected.ErrorPath != "" && !slices.ContainsFunc(data.Errors, func(issue service.ToolValidationIssue) bool {
					return issue.Path == expected.ErrorPath && strings.Contains(issue.Message, expected.ErrorContains)
				}) {
					t.Fatalf("expected error %s containing %q; got %s", expected.ErrorPath, expected.ErrorContains, out)
				}
				for _, check := range expected.Checks {
					if !slices.Contains(data.Checks, check) {
						t.Fatalf("missing check %q; got %s", check, out)
					}
				}
				for _, warning := range expected.WarningsContain {
					if !slices.ContainsFunc(data.Warnings, func(value string) bool { return strings.Contains(value, warning) }) {
						t.Fatalf("missing warning %q; got %s", warning, out)
					}
				}
			})
		}
	}
	if fixtures == 0 {
		t.Fatal("standalone fixture has no validation assertions")
	}
}

func containsValidationFixtureFields(actual, expected any) bool {
	if fields, ok := expected.(map[string]any); ok {
		object, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range fields {
			if !containsValidationFixtureFields(object[key], value) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(actual, expected)
}
