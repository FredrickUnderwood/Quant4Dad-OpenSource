package application

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
	"github.com/quant4dad/internal/repository/datasource"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

func integrationApplicationFixture(t *testing.T, background bool) (*IntegrationSettingsApplication, *service.IntegrationSettingService) {
	t.Helper()
	cfg, err := config.Parse([]byte("server:\n  addr: ':8080'\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := repository.NewIntegrationSettingsRepository(filepath.Join(t.TempDir(), "private", "integrations.yaml"), config.InitialIntegrations(cfg))
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.NewIntegrationSettingService(r)
	if err != nil {
		t.Fatal(err)
	}
	ds := NewDataSyncApp(cfg.Datasource, nil, nil, nil, datasource.Unavailable())
	auto, err := NewAutoSyncScheduler(ds, false, "03:00")
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewIntegrationSettingsApplication(s, ds, auto, func(c config.ArchiveConfig, cold coldstore.ColdStore) (*ArchiveScheduler, error) {
		return NewArchiveScheduler(nil, cold, c)
	}, background)
	if err != nil {
		t.Fatal(err)
	}
	return app, s
}
func TestIntegrationApplicationRejectsArchiveChangesWhileRunning(t *testing.T) {
	a, s := integrationApplicationFixture(t, false)
	d := s.Snapshot()
	revision := d.Revision
	a.archiveBusy = true
	in := &service.ArchiveIntegrationInput{DailyTime: "04:00", RetentionDays: 30, BatchSize: 1000, BatchSleepMS: 100}
	in.OSS.Prefix = "quant4dad/"
	if _, err := a.Save(context.Background(), service.IntegrationPatch{ExpectedRevision: &revision, Archive: in}); !errors.Is(err, ErrArchiveBusy) {
		t.Fatal("active archive configuration changed")
	}
	if s.Snapshot() != d {
		t.Fatal("busy rejection changed durable state")
	}
	a.archiveBusy = false
	view, err := a.Save(context.Background(), service.IntegrationPatch{ExpectedRevision: &revision, Archive: in})
	if err != nil || view.Archive.RetentionDays != 30 || view.Revision != 1 {
		t.Fatalf("save: %v", err)
	}
	if a.ManagedStatus().Configured || a.ManagedStatus().NextRunAt != nil || view.BackgroundEnabled {
		t.Fatal("no-background deployment started archive")
	}
	if _, _, err := a.RunOnce(context.Background()); !errors.Is(err, ErrArchiveUnconfigured) {
		t.Fatal("unconfigured archive accepted")
	}
}
func TestDataSyncTaskSnapshotKeepsOriginalConfiguration(t *testing.T) {
	first := config.DatasourceConfig{Provider: "first", InitialYears: 2, Concurrency: 1}
	second := config.DatasourceConfig{Provider: "second", InitialYears: 5, Concurrency: 7}
	a := NewDataSyncApp(first, nil, nil, nil, datasource.Unavailable())
	running := a.taskSnapshot()
	a.Configure(second, datasource.Unavailable())
	if running.cfg != first || a.taskSnapshot().cfg != second {
		t.Fatal("configuration change leaked into admitted task")
	}
}
func TestAutoSyncCanEnableAndDisableAfterStart(t *testing.T) {
	a, _ := NewAutoSyncScheduler(nil, false, "03:00")
	cancel := a.Start()
	defer cancel()
	if err := a.Update(true, time.Now().Add(time.Hour).Format("15:04")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for a.Status().NextRunAt == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !a.Status().Enabled || a.Status().NextRunAt == nil {
		t.Fatal("enabling did not schedule next run")
	}
	if err := a.Update(false, "03:00"); err != nil {
		t.Fatal(err)
	}
	if a.Status().Enabled || a.Status().NextRunAt != nil {
		t.Fatal("disabling retained scheduled execution")
	}
	before := a.Status()
	if err := a.Update(true, "invalid"); err == nil {
		t.Fatal("bad clock accepted")
	}
	if after := a.Status(); after.Enabled != before.Enabled || after.DailyTime != before.DailyTime {
		t.Fatal("invalid update changed scheduler")
	}
}

func TestIntegrationArchiveStatusRecordsPartialFailureAndSuccessfulRetry(t *testing.T) {
	a, _ := integrationApplicationFixture(t, false)
	db := archiveTestDB(t)
	day := time.Now().AddDate(0, 0, -10)
	seedEvent(t, db, "first-batch", day, 1, 1)
	seedEvent(t, db, "second-batch", day, 1, 1)
	injected := errors.New("synthetic-sensitive-upstream-diagnostic")
	manifests := 0
	store := &archiveHookStore{ColdStore: coldstore.NewLocalStore(t.TempDir())}
	store.put = func(key string) error {
		if strings.Contains(key, "/_manifest-") {
			manifests++
			if manifests == 2 {
				return injected
			}
		}
		return nil
	}
	a.archive = archiveScheduler(t, db, store)
	started := time.Now()
	days, events, err := a.RunOnce(context.Background())
	if !errors.Is(err, injected) || days != 0 || events != 1 || countRows(t, db, &domain.Event{}) != 1 {
		t.Fatalf("partial archive result: days=%d events=%d err=%v", days, events, err)
	}
	failed := a.ManagedStatus()
	if failed.Running || !failed.Configured || failed.LastRunAt == nil || failed.LastRunAt.Before(started) || failed.LastDays != days || failed.LastEvents != events || failed.LastError == "" || strings.Contains(failed.LastError, injected.Error()) {
		t.Fatal("partial archive failure was not recorded safely")
	}
	store.put = nil
	days, events, err = a.RunOnce(context.Background())
	if err != nil || days != 1 || events != 1 || countRows(t, db, &domain.Event{}) != 0 {
		t.Fatalf("archive retry result: days=%d events=%d err=%v", days, events, err)
	}
	succeeded := a.ManagedStatus()
	if succeeded.Running || succeeded.LastRunAt == nil || succeeded.LastRunAt.Before(*failed.LastRunAt) || succeeded.LastDays != days || succeeded.LastEvents != events || succeeded.LastError != "" {
		t.Fatal("successful archive retry did not replace failure status")
	}
}

const integrationQuotaProvider = "integration-quota-fixture"

var registerIntegrationQuotaProvider sync.Once

type integrationQuotaClient struct{ wait func(context.Context) error }

func (*integrationQuotaClient) Name() string                                         { return integrationQuotaProvider }
func (c *integrationQuotaClient) SetRequestLimiter(wait func(context.Context) error) { c.wait = wait }
func (c *integrationQuotaClient) ListInstruments(ctx context.Context) ([]*domain.Instrument, error) {
	if c.wait != nil {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func (*integrationQuotaClient) FetchBars(context.Context, string, domain.BarPeriod, time.Time, time.Time) ([]*domain.Bar, error) {
	return nil, errors.New("unexpected fixture bar request")
}

func TestIntegrationScheduleChangeKeepsClientAndSpentQuota(t *testing.T) {
	registerIntegrationQuotaProvider.Do(func() {
		datasource.Register(integrationQuotaProvider, func(config.DatasourceConfig) (datasource.Client, error) {
			return &integrationQuotaClient{}, nil
		})
	})
	a, s := integrationApplicationFixture(t, false)
	doc := s.Snapshot()
	in := &service.MarketIntegrationInput{Provider: integrationQuotaProvider, InitialYears: doc.Market.InitialYears, Concurrency: 1, RateLimitPerMin: 1, AutoSync: service.AutoSyncInput{DailyTime: "03:00"}}
	view, err := a.Save(context.Background(), service.IntegrationPatch{ExpectedRevision: &doc.Revision, Market: in})
	if err != nil {
		t.Fatal(err)
	}
	admitted := a.datasync.taskSnapshot()
	if _, err := admitted.client.ListInstruments(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Only the schedule changes. Spending the existing quota must still throttle
	// a newly admitted task even while an older task retains its client snapshot.
	in.AutoSync = service.AutoSyncInput{Enabled: true, DailyTime: "04:30"}
	view, err = a.Save(context.Background(), service.IntegrationPatch{ExpectedRevision: &view.Revision, Market: in})
	if err != nil || !view.Market.AutoSync.Enabled || view.Market.AutoSync.DailyTime != "04:30" {
		t.Fatal("schedule update failed", err)
	}
	current := a.datasync.taskSnapshot()
	if current.client != admitted.client || a.clients.Datasource != admitted.client {
		t.Fatal("schedule update replaced the active request limiter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := current.client.ListInstruments(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("schedule update reset spent request quota", err)
	}
	if a.auto.Status().NextRunAt != nil {
		t.Fatal("saving schedule bypassed no-background mode")
	}
}

func autoSyncAdmissionFixture(t *testing.T, beforeCreate func()) *AutoSyncScheduler {
	t.Helper()
	db := archiveTestDB(t)
	if err := db.AutoMigrate(&domain.DataSyncTask{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("integration:observe_admission", func(tx *gorm.DB) {
		if tx.Statement.Table == "data_sync_task" {
			beforeCreate()
			// Stop before the detached worker starts; only admission is under test.
			tx.AddError(errors.New("synthetic admission stop"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	tasks := service.NewDataSyncService(repository.NewDataSyncRepository(db))
	app := NewDataSyncApp(config.DatasourceConfig{InitialYears: 1, Concurrency: 1}, tasks, nil, nil, &integrationQuotaClient{})
	a, err := NewAutoSyncScheduler(app, true, "03:00")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAutoSyncStaleTimerCannotAdmitAfterConfigurationChange(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "rescheduled"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int64
			a := autoSyncAdmissionFixture(t, func() { attempts.Add(1) })
			stale := a.revision
			if err := a.Update(enabled, "04:00"); err != nil {
				t.Fatal(err)
			}
			a.triggerRevision(context.Background(), stale)
			if attempts.Load() != 0 || a.Status().LastRunAt != nil {
				t.Fatal("stale timer admitted an automatic task")
			}
			if err := a.Update(true, "05:00"); err != nil {
				t.Fatal(err)
			}
			a.triggerRevision(context.Background(), a.revision)
			if attempts.Load() != 1 || a.Status().LastRunAt == nil {
				t.Fatal("current timer did not reach task admission")
			}
		})
	}
}

func TestAutoSyncDisableWaitsForAdmissionGate(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var attempts atomic.Int64
	a := autoSyncAdmissionFixture(t, func() {
		if attempts.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	stale := a.revision
	triggered := make(chan struct{})
	go func() { a.triggerRevision(context.Background(), stale); close(triggered) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("timer did not reach admission")
	}
	disabling, disabled := make(chan struct{}), make(chan error, 1)
	go func() { close(disabling); disabled <- a.Update(false, "03:00") }()
	<-disabling
	select {
	case err := <-disabled:
		t.Fatal("disable returned while admission was still in progress", err)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-disabled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disable deadlocked with admission")
	}
	<-triggered
	a.triggerRevision(context.Background(), stale)
	if attempts.Load() != 1 || a.Status().Enabled {
		t.Fatal("an old timer admitted work after disable completed")
	}
}
