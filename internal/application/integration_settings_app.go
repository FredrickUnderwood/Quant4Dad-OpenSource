package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository/coldstore"
	"github.com/quant4dad/internal/service"
	"go.uber.org/zap"
)

var ErrArchiveUnconfigured = errors.New("OSS archive is not configured")
var ErrIntegrationProbe = errors.New("connection check failed; verify the saved endpoint, credentials and read permissions")

type ArchiveFactory func(config.ArchiveConfig, coldstore.ColdStore) (*ArchiveScheduler, error)

// IntegrationSettingsApplication owns live configuration and archive scheduling.
// Its single gate prevents changing an archive destination while a run is using
// it. The static ArchiveScheduler's Start is intentionally never called here.
type IntegrationSettingsApplication struct {
	mu             sync.Mutex
	settings       *service.IntegrationSettingService
	datasync       *DataSyncApp
	auto           *AutoSyncScheduler
	archiveFactory ArchiveFactory
	archive        *ArchiveScheduler
	clients        service.IntegrationClients
	background     bool
	archiveBusy    bool
	nextArchive    *time.Time
	lastArchive    *time.Time
	lastDays       int
	lastEvents     int64
	lastError      string
	changed        chan struct{}
}

func NewIntegrationSettingsApplication(settings *service.IntegrationSettingService, datasync *DataSyncApp, auto *AutoSyncScheduler, factory ArchiveFactory, background bool) (*IntegrationSettingsApplication, error) {
	if settings == nil || datasync == nil || auto == nil || factory == nil {
		return nil, service.ErrIntegrationInput
	}
	doc := settings.Snapshot()
	clients, err := settings.Prepare(doc)
	if err != nil {
		return nil, err
	}
	a := &IntegrationSettingsApplication{settings: settings, datasync: datasync, auto: auto, archiveFactory: factory, clients: clients, background: background, changed: make(chan struct{}, 1)}
	if clients.Coldstore != nil {
		a.archive, err = factory(doc.Archive, clients.Coldstore)
		if err != nil {
			return nil, err
		}
	}
	datasync.Configure(doc.Market.Datasource(), clients.Datasource)
	if err := auto.Update(doc.Market.AutoSync.Enabled, doc.Market.AutoSync.DailyTime); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *IntegrationSettingsApplication) View() service.IntegrationSettingsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return service.IntegrationView(a.settings.Snapshot(), a.background)
}
func (a *IntegrationSettingsApplication) Save(ctx context.Context, p service.IntegrationPatch) (service.IntegrationSettingsView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	previous := a.settings.Snapshot()
	doc, err := a.settings.Candidate(p)
	if err != nil {
		return service.IntegrationSettingsView{}, err
	}
	if p.Archive != nil && a.archiveBusy {
		return service.IntegrationSettingsView{}, ErrArchiveBusy
	}
	clients, err := a.settings.Prepare(doc)
	if err != nil {
		return service.IntegrationSettingsView{}, err
	}
	if previous.Market.Datasource() == doc.Market.Datasource() {
		clients.Datasource = a.clients.Datasource
	}
	archive := a.archive
	if p.Archive != nil {
		archive = nil
		if clients.Coldstore != nil {
			archive, err = a.archiveFactory(doc.Archive, clients.Coldstore)
			if err != nil {
				return service.IntegrationSettingsView{}, service.ErrIntegrationInput
			}
		}
	}
	doc, err = a.settings.Save(ctx, *p.ExpectedRevision, doc)
	if err != nil {
		return service.IntegrationSettingsView{}, err
	}
	if p.Market != nil {
		a.datasync.Configure(doc.Market.Datasource(), clients.Datasource)
		_ = a.auto.Update(doc.Market.AutoSync.Enabled, doc.Market.AutoSync.DailyTime)
		a.clients.Datasource = clients.Datasource
	}
	if p.Archive != nil {
		a.archive = archive
		a.clients.Coldstore = clients.Coldstore
		a.clients.OSSProbe = clients.OSSProbe
		a.nextArchive = nil
	}
	select {
	case a.changed <- struct{}{}:
	default:
	}
	return service.IntegrationView(doc, a.background), nil
}

func (a *IntegrationSettingsApplication) Probe(ctx context.Context, expected uint64, target string) error {
	a.mu.Lock()
	if a.settings.Snapshot().Revision != expected {
		a.mu.Unlock()
		return domain.ErrResourceConflict
	}
	var probe interface{ Probe(context.Context) error }
	if target == "market" {
		probe, _ = a.clients.Datasource.(interface{ Probe(context.Context) error })
	} else if target == "oss" {
		probe = a.clients.OSSProbe
	}
	a.mu.Unlock()
	if probe == nil {
		return errors.New("configure and save this integration before testing its connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := probe.Probe(ctx); err != nil {
		return ErrIntegrationProbe
	}
	return nil
}

type ManagedArchiveStatus struct {
	ArchiveStatus
	Configured bool `json:"configured"`
	Running    bool `json:"running"`
}

func (a *IntegrationSettingsApplication) ManagedStatus() ManagedArchiveStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	doc := a.settings.Snapshot()
	s := ArchiveStatus{Enabled: doc.Archive.Enabled, DailyTime: doc.Archive.DailyTime, RetentionDays: doc.Archive.RetentionDays}
	s.NextRunAt = a.nextArchive
	s.LastRunAt, s.LastDays, s.LastEvents, s.LastError = a.lastArchive, a.lastDays, a.lastEvents, a.lastError
	return ManagedArchiveStatus{ArchiveStatus: s, Configured: a.archive != nil, Running: a.archiveBusy}
}
func (a *IntegrationSettingsApplication) Status() ArchiveStatus {
	return a.ManagedStatus().ArchiveStatus
}
func (a *IntegrationSettingsApplication) RunOnce(ctx context.Context) (int, int64, error) {
	return a.runArchive(ctx, nil)
}
func (a *IntegrationSettingsApplication) runArchive(ctx context.Context, revision *uint64) (int, int64, error) {
	a.mu.Lock()
	doc := a.settings.Snapshot()
	if revision != nil && (doc.Revision != *revision || !doc.Archive.Enabled) {
		a.mu.Unlock()
		return 0, 0, nil
	}
	if a.archiveBusy {
		a.mu.Unlock()
		return 0, 0, ErrArchiveBusy
	}
	if a.archive == nil {
		a.mu.Unlock()
		return 0, 0, ErrArchiveUnconfigured
	}
	archive := a.archive
	a.archiveBusy = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.archiveBusy = false; a.mu.Unlock() }()
	days, events, err := archive.RunOnce(ctx)
	now := time.Now()
	a.mu.Lock()
	a.lastArchive, a.lastDays, a.lastEvents = &now, days, events
	a.lastError = ""
	if err != nil {
		a.lastError = "归档未完成，已验证的批次保留；请检查服务日志及 OSS 权限"
	}
	a.mu.Unlock()
	if err != nil {
		logger.L().Error("archive run failed", zap.Int("days", days), zap.Int64("events", events))
	} else {
		logger.L().Info("archive run completed", zap.Int("days", days), zap.Int64("events", events))
	}
	return days, events, err
}
func (a *IntegrationSettingsApplication) Start() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if a.background {
		go a.archiveLoop(ctx)
	}
	return cancel
}
func (a *IntegrationSettingsApplication) archiveLoop(ctx context.Context) {
	for {
		a.mu.Lock()
		doc := a.settings.Snapshot()
		enabled := doc.Archive.Enabled && a.archive != nil
		var wait time.Duration
		if enabled {
			t, _ := time.Parse("15:04", doc.Archive.DailyTime)
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			a.nextArchive = &next
			wait = time.Until(next)
		} else {
			a.nextArchive = nil
		}
		a.mu.Unlock()
		if !enabled {
			select {
			case <-ctx.Done():
				return
			case <-a.changed:
				continue
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.changed:
			timer.Stop()
			continue
		case <-timer.C:
			_, _, _ = a.runArchive(ctx, &doc.Revision)
		}
	}
}
