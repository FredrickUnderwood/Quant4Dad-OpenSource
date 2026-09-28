package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
)

func TestAgentToolMaintenanceRetainsIdentityAndLiveArtifacts(t *testing.T) {
	db := modelRepositoryDB(t)
	if err := db.AutoMigrate(&domain.AgentRequestBinding{}, &domain.AgentToolAudit{}, &domain.AgentToolEffect{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := NewAgentToolArtifactRepository(filepath.Join(parent, "results"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := artifacts.Put([]byte(`{"data":"retained"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(artifacts.directory, ref), old, old); err != nil {
		t.Fatal(err)
	}
	audits := NewAgentToolAuditRepository(db)
	rows := []domain.AgentToolAudit{
		{ID: "expired", ActorID: "a", RunID: "r", ToolCallID: "1", ToolName: "q", IdempotencyKey: "1", Status: "succeeded", Started: true, ResultRef: ref, CreatedAt: old, FinishedAt: &old},
		{ID: "retained", ActorID: "a", RunID: "r", ToolCallID: "2", ToolName: "q", IdempotencyKey: "2", Status: "succeeded", Started: true, ResultRef: ref, CreatedAt: now, FinishedAt: &now},
		{ID: "unknown", ActorID: "a", RunID: "missing", ToolCallID: "3", ToolName: "q", IdempotencyKey: "3", Status: "executing", Started: true, CreatedAt: old},
		{ID: "unstarted", ActorID: "a", RunID: "missing", ToolCallID: "4", ToolName: "q", IdempotencyKey: "4", Status: "executing", CreatedAt: old},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	changed, err := audits.Maintain(ctx, now, now.Add(-30*24*time.Hour))
	if err != nil || changed != 3 {
		t.Fatalf("settle: %d %v", changed, err)
	}
	var settled []domain.AgentToolAudit
	if err := db.Order("id").Find(&settled).Error; err != nil {
		t.Fatal(err)
	}
	if len(settled) != 4 || settled[0].Status != "expired" || settled[0].ResultRef != "" || settled[2].ErrorCode != "tool_outcome_unknown" || settled[3].ErrorCode != "agent_tool_not_started" {
		t.Fatalf("lost tombstones: %#v", settled)
	}
	if n, err := artifacts.Sweep(ctx, now, audits.ArtifactReferenced); err != nil || n != 0 {
		t.Fatalf("deleted shared live result: %d %v", n, err)
	}
	orphan, err := artifacts.Put([]byte(`{"data":"orphan"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(artifacts.directory, orphan), old, old); err != nil {
		t.Fatal(err)
	}
	active := domain.AgentToolAudit{ID: "active", ActorID: "a", RunID: "active", ToolCallID: "5", ToolName: "q", IdempotencyKey: "5", Status: "executing", Started: true}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := artifacts.Sweep(ctx, now, audits.ArtifactReferenced); err != nil || n != 0 {
		t.Fatalf("Put/Finish gap not protected: %d %v", n, err)
	}
	if err := audits.Finish(ctx, active.ID, "failed", "", "tool_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if n, err := artifacts.Sweep(ctx, now, audits.ArtifactReferenced); err != nil || n != 1 {
		t.Fatalf("orphan retained: %d %v", n, err)
	}
	if _, err := artifacts.Get(ref); err != nil {
		t.Fatal("retained artifact lost", err)
	}
	// A concurrent/cold writer refreshes the hash file before publishing a new
	// reference, so an old digest cannot be mistaken for an aged orphan.
	cold, err := NewAgentToolArtifactRepository(artifacts.directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cold.Put([]byte(`{"data":"orphan"}`)); err != nil {
		t.Fatal(err)
	}
	if n, err := artifacts.Sweep(ctx, now, audits.ArtifactReferenced); err != nil || n != 0 {
		t.Fatalf("fresh artifact removed: %d %v", n, err)
	}
	if err := audits.Finish(ctx, "unknown", "succeeded", orphan, ""); err == nil {
		t.Fatal("late success overwrote unknown outcome")
	}
}
