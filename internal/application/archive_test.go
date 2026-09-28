package application

import (
	"bufio"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
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
	if n := gzLineCount(t, filepath.Join(base15, "pipeline_event.jsonl.gz")); n != 2 {
		t.Errorf("dt=%s events lines = %d, want 2", dt15, n)
	}
	if n := gzLineCount(t, filepath.Join(base15, "pipeline_event_trace.jsonl.gz")); n != 3 {
		t.Errorf("dt=%s trace lines = %d, want 3", dt15, n)
	}
	if n := gzLineCount(t, filepath.Join(base15, "pipeline_ai_result.jsonl.gz")); n != 4 {
		t.Errorf("dt=%s ai lines = %d, want 4", dt15, n)
	}
	if _, err := os.Stat(filepath.Join(base15, "_manifest.json")); err != nil {
		t.Errorf("manifest missing: %v", err)
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
