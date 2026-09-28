package repository

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&domain.News{}, &domain.NewsSubscription{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func mkNews(source, ext string) *domain.News {
	return &domain.News{Source: source, ExternalID: ext, Title: "t-" + ext, PublishedAt: time.Now()}
}

func TestNewsInsertNewDedup(t *testing.T) {
	db := newTestDB(t)
	repo := NewNewsRepository(db)
	ctx := context.Background()

	// The first batch is entirely new.
	first, err := repo.InsertNew(ctx, "src-a", []*domain.News{
		mkNews("src-a", "1"),
		mkNews("src-a", "2"),
		mkNews("src-a", "2"), // duplicate within the batch, should be dropped
	})
	if err != nil {
		t.Fatalf("insert1: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first insert want 2 new, got %d", len(first))
	}

	// Second batch: 1 already exists, 3 is new.
	second, err := repo.InsertNew(ctx, "src-a", []*domain.News{
		mkNews("src-a", "1"),
		mkNews("src-a", "3"),
	})
	if err != nil {
		t.Fatalf("insert2: %v", err)
	}
	if len(second) != 1 || second[0].ExternalID != "3" {
		t.Fatalf("second insert want [3], got %+v", second)
	}

	// The same external_id from a different source does not collide.
	other, err := repo.InsertNew(ctx, "src-b", []*domain.News{
		mkNews("src-b", "1"),
	})
	if err != nil {
		t.Fatalf("insert3: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("cross-source want 1 new, got %d", len(other))
	}

	// Totals: 3 rows for src-a plus 1 for src-b.
	_, total, err := repo.List(ctx, NewsQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 4 {
		t.Fatalf("total want 4, got %d", total)
	}
}

func TestNewsSubscriptionReplace(t *testing.T) {
	db := newTestDB(t)
	subs := NewNewsSubscriptionRepository(db)
	ctx := context.Background()

	if err := subs.ReplaceForPipeline(db, 7, []string{"src-a", "src-b", "src-a"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := subs.ListSourcesByPipeline(ctx, 7)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 unique sources, got %d (%v)", len(got), got)
	}

	ids, err := subs.ListPipelineIDsBySource(ctx, "src-a")
	if err != nil {
		t.Fatalf("list ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("want [7], got %v", ids)
	}

	// Overwrite with src-c alone.
	if err := subs.ReplaceForPipeline(db, 7, []string{"src-c"}); err != nil {
		t.Fatalf("replace2: %v", err)
	}
	ids, _ = subs.ListPipelineIDsBySource(ctx, "src-a")
	if len(ids) != 0 {
		t.Fatalf("src-a should be unsubscribed, got %v", ids)
	}
}
