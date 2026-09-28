package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/quant4dad/internal/domain"
)

func TestResourceVersionsRejectStaleAndDeletedWrites(t *testing.T) {
	db := modelRepositoryDB(t)
	if err := db.AutoMigrate(&domain.Strategy{}, &domain.Pipeline{}, &domain.PipelineNode{}, &domain.PipelineEdge{}, &domain.NewsSubscription{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	strategyRepo := NewStrategyRepository(db)
	st := &domain.Strategy{Name: "original", Universe: domain.StringSlice{"SYNTHETIC"}, Body: []byte(`{}`)}
	if err := strategyRepo.Create(ctx, st); err != nil {
		t.Fatal(err)
	}
	stale, err := NewStrategyRepository(db).GetByID(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.Name = "accepted"
	if err := strategyRepo.Update(ctx, st); err != nil || st.Version != 2 {
		t.Fatalf("update: %v, version %d", err, st.Version)
	}
	stale.Name = "rejected"
	if err := strategyRepo.Update(ctx, stale); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatalf("stale strategy: %v", err)
	}
	if err := strategyRepo.Delete(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := strategyRepo.Update(ctx, st); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatalf("deleted strategy: %v", err)
	}

	pipelineRepo := NewPipelineRepository(db)
	p := &domain.Pipeline{Name: "original", Sources: []string{"synthetic"}, Nodes: []domain.PipelineNode{{NodeKey: "old", Type: "synthetic", Config: []byte(`{}`)}}}
	if err := pipelineRepo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	old, err := NewPipelineRepository(db).GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A status-only update invalidates both an editor snapshot and an approval.
	if err := pipelineRepo.UpdateStatus(ctx, p.ID, domain.PipelineStatusEnabled, p.Version); err != nil {
		t.Fatal(err)
	}
	old.Name = "rejected"
	old.Nodes[0].NodeKey = "rejected"
	old.Sources = []string{"rejected"}
	if err := pipelineRepo.Update(ctx, old); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatalf("stale pipeline: %v", err)
	}
	current, err := pipelineRepo.GetByID(ctx, p.ID)
	if err != nil || current.Version != 2 || current.Name != "original" || current.Status != domain.PipelineStatusEnabled || len(current.Nodes) != 1 || current.Nodes[0].NodeKey != "old" || len(current.Sources) != 1 || current.Sources[0] != "synthetic" {
		t.Fatalf("stale write changed children/status: %#v %v", current, err)
	}
	current.Name = "accepted"
	current.Nodes[0].NodeKey = "accepted"
	if err := pipelineRepo.Update(ctx, current); err != nil || current.Version != 3 {
		t.Fatalf("valid update: %v", err)
	}
	if err := pipelineRepo.UpdateStatus(ctx, p.ID, domain.PipelineStatusDisabled, 2); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatalf("stale status: %v", err)
	}
	if err := pipelineRepo.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := pipelineRepo.Update(ctx, current); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatalf("deleted pipeline: %v", err)
	}
	var children int64
	if err := db.Model(&domain.PipelineNode{}).Where("pipeline_id = ?", p.ID).Count(&children).Error; err != nil || children != 0 {
		t.Fatalf("resurrected children: %d %v", children, err)
	}
}
