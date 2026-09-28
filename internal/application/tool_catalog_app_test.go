package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quant4dad/internal/service"
)

func TestToolCatalogScopeSnapshotTimeoutAndResultLimits(t *testing.T) {
	calls := 0
	catalog := NewToolCatalogApplication()
	definition := readTool("query", "description", objectSchema(map[string]any{}), map[string]any{"type": "string"}, []string{"external"}, func(context.Context, []byte) (any, error) { calls++; return "answer", nil })
	catalog.Register(definition)
	revision := catalog.Revision("external")
	definition.InputSchema["malicious"] = true
	copy := catalog.Definitions("external")
	copy[0].Profiles[0] = "admin"
	copy[0].InputSchema["malicious"] = true
	if catalog.Revision("external") != revision || len(catalog.Definitions("admin")) != 0 {
		t.Fatal("mutable policy escaped")
	}
	if _, err := catalog.Invoke(context.Background(), "admin", "query", []byte("{}")); !errors.Is(err, ErrToolForbidden) {
		t.Fatal(err)
	}
	for _, args := range []string{`{"run_id":"forged"}`, `{"_meta":{}}`, `{"a":1,"a":2}`, `null`} {
		if _, err := catalog.Invoke(context.Background(), "external", "query", []byte(args)); !errors.Is(err, service.ErrToolInput) {
			t.Fatal("invalid context accepted", err)
		}
	}
	if calls != 0 {
		t.Fatal("rejected invocation reached business handler")
	}
	if _, err := catalog.Invoke(context.Background(), "external", "query", []byte("{}")); err != nil || calls != 1 {
		t.Fatal(err)
	}
	limited := NewToolCatalogApplication()
	definition.Name = "large"
	definition.MaxResult = 64
	definition.Handler = func(context.Context, []byte) (any, error) { return strings.Repeat("x", 65), nil }
	limited.Register(definition)
	if _, err := limited.Invoke(context.Background(), "external", "large", nil); !errors.Is(err, ErrToolResultTooLarge) {
		t.Fatal(err)
	}
	definition.Name = "slow"
	definition.TimeoutMS = 1
	definition.Handler = func(ctx context.Context, _ []byte) (any, error) { <-ctx.Done(); return "late-success", nil }
	limited.Register(definition)
	if _, err := limited.Invoke(context.Background(), "external", "slow", nil); !errors.Is(err, ErrToolTimeout) {
		t.Fatal("late result accepted", err)
	}
}
