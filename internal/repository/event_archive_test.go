package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func eventArchiveDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "archive.db")+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := db.DB()
	t.Cleanup(func() { _ = raw.Close() })
	return db
}
func completedArchiveEvent(t *testing.T, db *gorm.DB) *domain.Event {
	t.Helper()
	now := time.Now().UTC().AddDate(0, 0, -10)
	event := &domain.Event{EventUID: "archive", PipelineID: 1, Status: domain.EventStatusProcessing, ReceivedAt: now, RawPayload: []byte(`{"fixture":true}`)}
	if err := db.Create(event).Error; err != nil {
		t.Fatal(err)
	}
	event.Status = domain.EventStatusPassed
	event.FinishedAt = &now
	if err := NewEventRepository(db).SaveResult(context.Background(), event, []domain.EventTrace{{EventID: event.ID, NodeKey: "a", Action: "pass"}}, []domain.AIResult{{EventID: event.ID, NodeKey: "ai", Output: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	return event
}
func eventSnapshot(t *testing.T, db *gorm.DB, event *domain.Event) ArchiveSnapshot {
	t.Helper()
	snapshot, err := NewEventRepository(db).ReadArchiveBatch(context.Background(), event.ReceivedAt.Add(-time.Hour), event.ReceivedAt.Add(time.Hour), 0, event.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 {
		t.Fatal("missing snapshot")
	}
	return snapshot
}
func eventRowCount(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestArchiveSnapshotIsConsistentAcrossConcurrentChildInsert(t *testing.T) {
	db := eventArchiveDB(t)
	event := completedArchiveEvent(t, db)
	var fired atomic.Bool
	if err := db.Callback().Query().After("gorm:query").Register("archive_concurrent_insert", func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_event" && fired.CompareAndSwap(false, true) {
			if err := db.Create(&domain.EventTrace{EventID: event.ID, NodeKey: "late", Action: "pass"}).Error; err != nil {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := eventSnapshot(t, db, event)
	if len(snapshot.Traces) != 1 || eventRowCount(t, db, &domain.EventTrace{}) != 2 {
		t.Fatal("archive read did not retain its transaction snapshot")
	}
	deleted, err := NewEventRepository(db).DeleteArchiveSnapshot(context.Background(), snapshot)
	if !errors.Is(err, ErrArchiveSnapshotChanged) || deleted != 0 || eventRowCount(t, db, &domain.Event{}) != 1 {
		t.Fatalf("late child was not protected: %d %v", deleted, err)
	}
}
func TestArchiveSnapshotConcurrentDeleteIsIdempotent(t *testing.T) {
	db := eventArchiveDB(t)
	event := completedArchiveEvent(t, db)
	snapshot := eventSnapshot(t, db, event)
	repo := NewEventRepository(db)
	var wg sync.WaitGroup
	var total atomic.Int64
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := repo.DeleteArchiveSnapshot(context.Background(), snapshot)
			total.Add(n)
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total.Load() != 1 {
		t.Fatal("deleted twice", total.Load())
	}
	for _, model := range []any{&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}} {
		if eventRowCount(t, db, model) != 0 {
			t.Fatal("partial deletion")
		}
	}
}

func TestArchiveSnapshotRejectsProcessingDeletion(t *testing.T) {
	db := eventArchiveDB(t)
	event := &domain.Event{EventUID: "processing", PipelineID: 1, Status: domain.EventStatusProcessing, ReceivedAt: time.Now()}
	if err := db.Create(event).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := emptyArchiveSnapshot()
	snapshot.Events = append(snapshot.Events, event)
	deleted, err := NewEventRepository(db).DeleteArchiveSnapshot(context.Background(), snapshot)
	if !errors.Is(err, ErrArchiveSnapshotChanged) || deleted != 0 || eventRowCount(t, db, &domain.Event{}) != 1 {
		t.Fatalf("processing event deleted: %d %v", deleted, err)
	}
}
func TestArchiveSnapshotDeletionRollsBackAllTables(t *testing.T) {
	db := eventArchiveDB(t)
	event := completedArchiveEvent(t, db)
	snapshot := eventSnapshot(t, db, event)
	injected := errors.New("injected_parent_delete_failure")
	if err := db.Callback().Delete().Before("gorm:delete").Register("archive_delete_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_event" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := NewEventRepository(db).DeleteArchiveSnapshot(context.Background(), snapshot)
	if !errors.Is(err, injected) || deleted != 0 {
		t.Fatalf("delete result: %d %v", deleted, err)
	}
	for _, model := range []any{&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}} {
		if eventRowCount(t, db, model) != 1 {
			t.Fatal("deletion transaction was partial")
		}
	}
}
func TestEventResultCannotRewriteTerminalOrCreateOrphans(t *testing.T) {
	db := eventArchiveDB(t)
	event := completedArchiveEvent(t, db)
	repo := NewEventRepository(db)
	trace := []domain.EventTrace{{EventID: event.ID, NodeKey: "late", Action: "pass"}}
	ai := []domain.AIResult{{EventID: event.ID, NodeKey: "late"}}
	if err := repo.SaveResult(context.Background(), event, trace, ai); !errors.Is(err, ErrEventResultState) {
		t.Fatal("terminal result rewritten", err)
	}
	if eventRowCount(t, db, &domain.EventTrace{}) != 1 || eventRowCount(t, db, &domain.AIResult{}) != 1 {
		t.Fatal("duplicate children inserted")
	}
	snapshot := eventSnapshot(t, db, event)
	if _, err := repo.DeleteArchiveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveResult(context.Background(), event, trace, ai); !errors.Is(err, ErrEventResultState) {
		t.Fatal("missing parent accepted", err)
	}
	for _, model := range []any{&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}} {
		if eventRowCount(t, db, model) != 0 {
			t.Fatal("orphan record inserted")
		}
	}
}
func TestEventResultRejectsMismatchedChildIdentity(t *testing.T) {
	db := eventArchiveDB(t)
	event := &domain.Event{EventUID: "identity", PipelineID: 1, Status: domain.EventStatusProcessing, ReceivedAt: time.Now()}
	if err := db.Create(event).Error; err != nil {
		t.Fatal(err)
	}
	event.Status = domain.EventStatusPassed
	if err := NewEventRepository(db).SaveResult(context.Background(), event, []domain.EventTrace{{EventID: event.ID + 1}}, nil); !errors.Is(err, ErrEventResultState) {
		t.Fatal(err)
	}
	var stored domain.Event
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.EventStatusProcessing || eventRowCount(t, db, &domain.EventTrace{}) != 0 {
		t.Fatal("mismatched child changed parent")
	}
}
