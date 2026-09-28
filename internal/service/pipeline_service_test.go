package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline"
	"github.com/quant4dad/internal/repository"
)

// stubProc is a test node: a fixed action, able to write a field into the payload so
// conditional routing can be driven.
type stubProc struct {
	typ    string
	action pipeline.Action
	writes map[string]any
}

func (s *stubProc) Type() string                   { return s.typ }
func (s *stubProc) ConfigSchema() json.RawMessage  { return json.RawMessage(`{}`) }
func (s *stubProc) Validate(json.RawMessage) error { return nil }
func (s *stubProc) Process(_ context.Context, rc *pipeline.RunContext, _ json.RawMessage) (pipeline.Action, error) {
	for k, v := range s.writes {
		rc.Msg.Payload[k] = v
	}
	return s.action, nil
}

func svcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "svc.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db
}

func traceKeys(res pipeline.ExecResult) map[string]bool {
	m := map[string]bool{}
	for _, tr := range res.Traces {
		m[tr.NodeKey] = true
	}
	return m
}

// End to end: Create persists the edge conditions, GetByID loads them back, and DryRun
// routes on those conditions via ToEngine plus the executor. start writes route=x, so
// the start->A edge (route==x) matches and runs while the start->B edge (route==y)
// does not and is pruned.
func TestPipelineService_ConditionRoutingEndToEnd(t *testing.T) {
	db := svcTestDB(t)
	reg := pipeline.NewRegistry()
	reg.Register(&stubProc{typ: "start", action: pipeline.ActionPass, writes: map[string]any{"route": "x"}})
	reg.Register(&stubProc{typ: "sink", action: pipeline.ActionPass})
	svc := NewPipelineService(repository.NewPipelineRepository(db), reg)

	in := PipelineInput{
		Name:   "t",
		Status: domain.PipelineStatusEnabled,
		Nodes: []NodeInput{
			{NodeKey: "start", Type: "start"},
			{NodeKey: "A", Type: "sink"},
			{NodeKey: "B", Type: "sink"},
		},
		Edges: []EdgeInput{
			{FromNodeKey: "start", ToNodeKey: "A", Condition: json.RawMessage(`{"field":"route","op":"eq","value":"x"}`)},
			{FromNodeKey: "start", ToNodeKey: "B", Condition: json.RawMessage(`{"field":"route","op":"eq","value":"y"}`)},
		},
	}
	created, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := svc.DryRun(context.Background(), created.ID, map[string]any{})
	if err != nil {
		t.Fatalf("dryrun: %v", err)
	}
	keys := traceKeys(res)
	if !keys["start"] || !keys["A"] {
		t.Errorf("start and A should run, traces=%v", keys)
	}
	if keys["B"] {
		t.Errorf("B should be pruned when its condition does not match, traces=%v", keys)
	}
	if res.Status != "passed" {
		t.Errorf("status=%s, want passed", res.Status)
	}
}

// An invalid edge condition must be rejected by validation at save time.
func TestPipelineService_RejectsBadEdgeCondition(t *testing.T) {
	db := svcTestDB(t)
	reg := pipeline.NewRegistry()
	reg.Register(&stubProc{typ: "sink", action: pipeline.ActionPass})
	svc := NewPipelineService(repository.NewPipelineRepository(db), reg)

	in := PipelineInput{
		Name:   "bad",
		Status: domain.PipelineStatusDraft,
		Nodes:  []NodeInput{{NodeKey: "a", Type: "sink"}, {NodeKey: "b", Type: "sink"}},
		Edges:  []EdgeInput{{FromNodeKey: "a", ToNodeKey: "b", Condition: json.RawMessage(`{"op":"eq","value":1}`)}},
	}
	if _, err := svc.Create(context.Background(), in); err == nil {
		t.Fatal("a condition missing field should be rejected")
	}
}
