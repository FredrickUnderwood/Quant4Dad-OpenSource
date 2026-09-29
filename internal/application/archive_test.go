package application

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"github.com/bytedance/sonic"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
)

func archiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "arch.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// seedEvent inserts one event plus nTrace lineage rows and nAI AI-result rows.
func seedEvent(t *testing.T, db *gorm.DB, uid string, received time.Time, nTrace, nAI int) int64 {
	t.Helper()
	e := &domain.Event{
		EventUID:   uid,
		PipelineID: 1,
		Source:     "test",
		RawPayload: []byte(`{"k":"v"}`),
		Status:     domain.EventStatusPassed,
		ReceivedAt: received,
	}
	if err := db.Create(e).Error; err != nil {
		t.Fatalf("create event: %v", err)
	}
	for i := 0; i < nTrace; i++ {
		if err := db.Create(&domain.EventTrace{EventID: e.ID, NodeKey: "n", Action: "pass", CreatedAt: received}).Error; err != nil {
			t.Fatalf("create trace: %v", err)
		}
	}
	for i := 0; i < nAI; i++ {
		if err := db.Create(&domain.AIResult{EventID: e.ID, NodeKey: "ai", Output: []byte(`{}`), CreatedAt: received}).Error; err != nil {
			t.Fatalf("create ai: %v", err)
		}
	}
	return e.ID
}

func countRows(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func gzLineCount(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	defer gz.Close()
	n := 0
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return n
}

func TestArchiveRunOnce(t *testing.T) {
	db := archiveTestDB(t)
	eventRepo := repository.NewEventRepository(db)

	now := time.Now()
	day := func(d int) time.Time {
		base := now.AddDate(0, 0, d)
		return time.Date(base.Year(), base.Month(), base.Day(), 10, 0, 0, 0, base.Location())
	}

	// Two expired days (two rows at -15d, one at -12d) plus one hot day (one row at -2d,
	// which must be left alone).
	seedEvent(t, db, "old-a", day(-15), 2, 1)
	seedEvent(t, db, "old-b", day(-15), 1, 3)
	seedEvent(t, db, "old-c", day(-12), 0, 2)
	hotID := seedEvent(t, db, "hot", day(-2), 1, 1)

	coldDir := t.TempDir()
	cold := coldstore.NewLocalStore(coldDir)
	sched, err := NewArchiveScheduler(eventRepo, cold, config.ArchiveConfig{
		Enabled:       true,
		DailyTime:     "03:30",
		RetentionDays: 10,
		BatchSize:     2, // a small batch, to force paging
		BatchSleepMS:  0,
		OSS:           config.OSSConfig{Prefix: "test/"},
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}

	days, events, err := sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if days != 2 {
		t.Errorf("days = %d, want 2", days)
	}
	if events != 3 {
		t.Errorf("events = %d, want 3 (old-a/b/c)", events)
	}

	// The expired data should be gone from MySQL, leaving only the hot row and its children.
	if got := countRows(t, db, &domain.Event{}); got != 1 {
		t.Errorf("remaining events = %d, want 1", got)
	}
	if got := countRows(t, db, &domain.EventTrace{}); got != 1 {
		t.Errorf("remaining traces = %d, want 1 (only hot)", got)
	}
	if got := countRows(t, db, &domain.AIResult{}); got != 1 {
		t.Errorf("remaining ai = %d, want 1 (only hot)", got)
	}
	var hot domain.Event
	if err := db.First(&hot, hotID).Error; err != nil {
		t.Errorf("hot event should survive: %v", err)
	}

	// The cold store objects should exist with row counts matching what was exported: the
	// -15d day has 2 events / 3 traces / 4 ai results.
	dt15 := day(-15).Format("2006-01-02")
	base15 := filepath.Join(coldDir, "test", "event", "dt="+dt15)
	if n := archivedRows(t, base15, "pipeline_event-"); n != 2 {
		t.Errorf("dt=%s events lines = %d, want 2", dt15, n)
	}
	if n := archivedRows(t, base15, "pipeline_event_trace-"); n != 3 {
		t.Errorf("dt=%s trace lines = %d, want 3", dt15, n)
	}
	if n := archivedRows(t, base15, "pipeline_ai_result-"); n != 4 {
		t.Errorf("dt=%s ai lines = %d, want 4", dt15, n)
	}
	if manifests, err := filepath.Glob(filepath.Join(base15, "batch=*", "_manifest-*.json")); err != nil || len(manifests) != 1 {
		t.Fatalf("manifest missing: %v %v", manifests, err)
	}

	// A second run should be a no-op, having caught up.
	days2, events2, err := sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce#2: %v", err)
	}
	if days2 != 0 || events2 != 0 {
		t.Errorf("second run = (%d days, %d events), want (0,0)", days2, events2)
	}
}

func archivedRows(t *testing.T, base, prefix string) int {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(base, "batch=*", prefix+"*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, path := range paths {
		total += gzLineCount(t, path)
	}
	return total
}
func archiveFiles(t *testing.T, base string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(base, func(path string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !item.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = body
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type archiveHookStore struct {
	coldstore.ColdStore
	put    func(string) error
	verify func(string) error
}

func (s *archiveHookStore) PutImmutable(ctx context.Context, key string, r io.ReadSeeker, digest coldstore.Digest) error {
	if s.put != nil {
		if err := s.put(key); err != nil {
			return err
		}
	}
	return s.ColdStore.PutImmutable(ctx, key, r, digest)
}
func (s *archiveHookStore) Verify(ctx context.Context, key string, digest coldstore.Digest) error {
	if s.verify != nil {
		if err := s.verify(key); err != nil {
			return err
		}
	}
	return s.ColdStore.Verify(ctx, key, digest)
}
func archiveScheduler(t *testing.T, db *gorm.DB, store coldstore.ColdStore) *ArchiveScheduler {
	t.Helper()
	scheduler, err := NewArchiveScheduler(repository.NewEventRepository(db), store, config.ArchiveConfig{Enabled: true, RetentionDays: 1, BatchSize: 1, DailyTime: "03:30", OSS: config.OSSConfig{Prefix: "test/"}})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}
func TestArchiveRetryPreservesPreviouslyPublishedBatches(t *testing.T) {
	db := archiveTestDB(t)
	day := time.Now().AddDate(0, 0, -10)
	for _, uid := range []string{"a", "b", "c"} {
		seedEvent(t, db, uid, day, 1, 1)
	}
	directory := t.TempDir()
	store := &archiveHookStore{ColdStore: coldstore.NewLocalStore(directory)}
	manifests := 0
	store.put = func(key string) error {
		if strings.Contains(key, "/_manifest-") {
			manifests++
			if manifests == 2 {
				return errors.New("injected_upload_failure")
			}
		}
		return nil
	}
	scheduler := archiveScheduler(t, db, store)
	_, deleted, err := scheduler.RunOnce(context.Background())
	if err == nil || deleted != 1 || countRows(t, db, &domain.Event{}) != 2 {
		t.Fatalf("partial run: deleted=%d err=%v", deleted, err)
	}
	before := archiveFiles(t, directory)
	store.put = nil
	_, deleted, err = scheduler.RunOnce(context.Background())
	if err != nil || deleted != 2 || countRows(t, db, &domain.Event{}) != 0 {
		t.Fatalf("retry: %d %v", deleted, err)
	}
	after := archiveFiles(t, directory)
	for key, body := range before {
		if !bytes.Equal(body, after[key]) {
			t.Fatal("retry changed an earlier object", key)
		}
	}
	manifestsOnDisk, err := filepath.Glob(filepath.Join(directory, "test/event/dt=*", "batch=*", "_manifest-*.json"))
	if err != nil || len(manifestsOnDisk) != 3 {
		t.Fatalf("three immutable batch manifests required: %v %v", manifestsOnDisk, err)
	}
	ids := map[int64]bool{}
	for _, file := range manifestsOnDisk {
		body, _ := os.ReadFile(file)
		var manifest archiveBatchManifest
		if err := sonic.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, id := range manifest.EventIDs {
			if ids[id] {
				t.Fatal("duplicate logical batch", id)
			}
			ids[id] = true
		}
	}
	if len(ids) != 3 {
		t.Fatal("lost archived identity", ids)
	}
}
func TestArchiveVerificationFailureDoesNotDelete(t *testing.T) {
	db := archiveTestDB(t)
	seedEvent(t, db, "verify", time.Now().AddDate(0, 0, -10), 1, 1)
	store := &archiveHookStore{ColdStore: coldstore.NewLocalStore(t.TempDir()), verify: func(key string) error {
		if strings.Contains(key, "/_manifest-") {
			return coldstore.ErrObjectConflict
		}
		return nil
	}}
	_, deleted, err := archiveScheduler(t, db, store).RunOnce(context.Background())
	if !errors.Is(err, coldstore.ErrObjectConflict) || deleted != 0 {
		t.Fatalf("unverified deletion: %d %v", deleted, err)
	}
	for _, model := range []any{&domain.Event{}, &domain.EventTrace{}, &domain.AIResult{}} {
		if countRows(t, db, model) != 1 {
			t.Fatal("verification failure deleted source rows")
		}
	}
}
func TestArchiveConcurrentChangesAbortExactDeletion(t *testing.T) {
	for _, change := range []string{"event", "trace", "ai_result", "new_trace"} {
		t.Run(change, func(t *testing.T) {
			db := archiveTestDB(t)
			id := seedEvent(t, db, change, time.Now().AddDate(0, 0, -10), 1, 1)
			changed := false
			store := &archiveHookStore{ColdStore: coldstore.NewLocalStore(t.TempDir())}
			store.verify = func(key string) error {
				if changed || !strings.Contains(key, "/_manifest-") {
					return nil
				}
				changed = true
				switch change {
				case "event":
					return db.Model(&domain.Event{}).Where("id = ?", id).Update("source", "changed").Error
				case "trace":
					return db.Model(&domain.EventTrace{}).Where("event_id = ?", id).Update("error", "changed").Error
				case "ai_result":
					return db.Model(&domain.AIResult{}).Where("event_id = ?", id).Update("model", "changed").Error
				default:
					return db.Create(&domain.EventTrace{EventID: id, NodeKey: "late", Action: "pass"}).Error
				}
			}
			_, deleted, err := archiveScheduler(t, db, store).RunOnce(context.Background())
			if !errors.Is(err, repository.ErrArchiveSnapshotChanged) || deleted != 0 {
				t.Fatalf("changed snapshot deleted: %d %v", deleted, err)
			}
			if countRows(t, db, &domain.Event{}) != 1 || countRows(t, db, &domain.AIResult{}) != 1 {
				t.Fatal("source removed despite changed snapshot")
			}
		})
	}
}
func TestArchiveDayDoesNotDeleteConcurrentNewEvent(t *testing.T) {
	db := archiveTestDB(t)
	old := time.Now().AddDate(0, 0, -10)
	day := time.Date(old.Year(), old.Month(), old.Day(), 0, 0, 0, 0, old.Location())
	seedEvent(t, db, "initial", day, 1, 1)
	var late int64
	store := &archiveHookStore{ColdStore: coldstore.NewLocalStore(t.TempDir())}
	store.verify = func(key string) error {
		if late == 0 && strings.Contains(key, "/_manifest-") {
			late = seedEvent(t, db, "late", day, 1, 1)
		}
		return nil
	}
	deleted, err := archiveScheduler(t, db, store).archiveDay(context.Background(), day, day.AddDate(0, 0, 1))
	if err != nil || deleted != 1 {
		t.Fatalf("archive day: %d %v", deleted, err)
	}
	var found domain.Event
	if err := db.First(&found, late).Error; err != nil {
		t.Fatal("late unexported event deleted", err)
	}
	if countRows(t, db, &domain.EventTrace{}) != 1 || countRows(t, db, &domain.AIResult{}) != 1 {
		t.Fatal("late child records deleted")
	}
}
func TestArchiveProcessingEventsRemainHot(t *testing.T) {
	db := archiveTestDB(t)
	id := seedEvent(t, db, "processing", time.Now().AddDate(0, 0, -10), 0, 0)
	if err := db.Model(&domain.Event{}).Where("id = ?", id).Update("status", domain.EventStatusProcessing).Error; err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	days, deleted, err := archiveScheduler(t, db, coldstore.NewLocalStore(directory)).RunOnce(context.Background())
	if err != nil || days != 0 || deleted != 0 || countRows(t, db, &domain.Event{}) != 1 || len(archiveFiles(t, directory)) != 0 {
		t.Fatalf("processing event archived: %d %d %v", days, deleted, err)
	}
}
