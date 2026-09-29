// Package application: archive.go is the hot/cold tiering scheduler for the three event
// tables. At the scheduled time each day it exports whole days of events whose
// received_at falls outside the retention window — along with their traces and
// ai_results — to object storage as jsonl.gz, and only after verifying the upload does
// it delete them from the database in throttled batches, leaving the live database carrying
// hot data only.
package application

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
)

// ErrArchiveBusy means a round of archiving is already running (the daily job or a
// manual trigger), so a second concurrent round is refused rather than have two rounds
// scan and delete the same data at once.
var ErrArchiveBusy = errors.New("archive: a run is already in progress")

// maxDaysPerRun caps how many days one archiving run processes. A fresh deployment can
// have a large backlog of historical days; they are drained and committed a day at a
// time, and this cap keeps a single run from taking hours. Whatever is left is picked up
// by the next run, the following day.
const maxDaysPerRun = 60

// ArchiveStatus exposes the run state, for the handler and for diagnosis.
type ArchiveStatus struct {
	Enabled       bool       `json:"enabled"`
	DailyTime     string     `json:"daily_time"`
	RetentionDays int        `json:"retention_days"`
	NextRunAt     *time.Time `json:"next_run_at"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastDays      int        `json:"last_days"`   // days archived by the last round
	LastEvents    int64      `json:"last_events"` // events archived and deleted by the last round
	LastError     string     `json:"last_error,omitempty"`
}

// ArchiveScheduler triggers one archive job per day at daily_time.
type ArchiveScheduler struct {
	eventRepo *repository.EventRepository
	cold      coldstore.ColdStore

	enabled       bool
	hour, min     int
	retentionDays int
	batchSize     int
	batchSleep    time.Duration
	prefix        string

	running atomic.Bool // keeps the daily job and a manual trigger from running concurrently

	mu         sync.Mutex
	nextRunAt  *time.Time
	lastRunAt  *time.Time
	lastDays   int
	lastEvents int64
	lastErr    string
}

// NewArchiveScheduler validates daily_time and wires up the scheduler. A nil cold means
// no cold store is configured; callers must pass a non-nil cold when enabled.
func NewArchiveScheduler(eventRepo *repository.EventRepository, cold coldstore.ColdStore, cfg config.ArchiveConfig) (*ArchiveScheduler, error) {
	h, m, err := parseDailyTime(cfg.DailyTime)
	if err != nil {
		return nil, fmt.Errorf("archive.daily_time: %w", err)
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 2000
	}
	prefix := cfg.OSS.Prefix
	return &ArchiveScheduler{
		eventRepo:     eventRepo,
		cold:          cold,
		enabled:       cfg.Enabled,
		hour:          h,
		min:           m,
		retentionDays: cfg.RetentionDays,
		batchSize:     batch,
		batchSleep:    time.Duration(cfg.BatchSleepMS) * time.Millisecond,
		prefix:        prefix,
	}, nil
}

// Status is for the handler.
func (s *ArchiveScheduler) Status() ArchiveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ArchiveStatus{
		Enabled:       s.enabled,
		DailyTime:     fmt.Sprintf("%02d:%02d", s.hour, s.min),
		RetentionDays: s.retentionDays,
		NextRunAt:     s.nextRunAt,
		LastRunAt:     s.lastRunAt,
		LastDays:      s.lastDays,
		LastEvents:    s.lastEvents,
		LastError:     s.lastErr,
	}
}

// Start launches the scheduling goroutine, which sleeps until the next daily_time and
// then fires; it returns a cancel for shutdown. With Enabled=false it returns a no-op
// cancel.
func (s *ArchiveScheduler) Start() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if !s.enabled {
		return cancel
	}
	go s.loop(ctx)
	return cancel
}

func (s *ArchiveScheduler) loop(ctx context.Context) {
	for {
		next := s.computeNext(time.Now())
		s.mu.Lock()
		s.nextRunAt = &next
		s.mu.Unlock()

		wait := time.Until(next)
		logger.L().Info("archive scheduled", zap.Time("next_run_at", next), zap.Duration("wait", wait))

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.trigger(ctx)
		}
	}
}

func (s *ArchiveScheduler) computeNext(now time.Time) time.Time {
	loc := now.Location()
	candidate := time.Date(now.Year(), now.Month(), now.Day(), s.hour, s.min, 0, 0, loc)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func (s *ArchiveScheduler) trigger(ctx context.Context) {
	days, events, err := s.RunOnce(ctx)
	now := time.Now()
	s.mu.Lock()
	s.lastRunAt = &now
	s.lastDays = days
	s.lastEvents = events
	if err != nil {
		s.lastErr = err.Error()
		logger.L().Error("archive run failed", zap.Int("days", days), zap.Int64("events", events), zap.Error(err))
	} else {
		s.lastErr = ""
		logger.L().Info("archive run done", zap.Int("days", days), zap.Int64("events", events))
	}
	s.mu.Unlock()
}

// RunOnce performs one round of archiving: starting from the oldest expired day, it
// exports and deletes a day at a time until it catches up with the retention window or
// hits the per-run day cap. It returns how many days were processed and how many events
// were deleted in total. The handler can trigger it manually.
func (s *ArchiveScheduler) RunOnce(ctx context.Context) (days int, events int64, err error) {
	if s.cold == nil {
		return 0, 0, coldstore.ErrStoreUnavailable
	}
	if !s.running.CompareAndSwap(false, true) {
		return 0, 0, ErrArchiveBusy
	}
	defer s.running.Store(false)

	// Keep the last retentionDays of hot data: archive whole days before
	// (today's midnight - retentionDays).
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	archiveBefore := today.AddDate(0, 0, -s.retentionDays)

	for days < maxDaysPerRun {
		select {
		case <-ctx.Done():
			return days, events, ctx.Err()
		default:
		}

		day, found, e := s.eventRepo.OldestEventDayBefore(ctx, archiveBefore)
		if e != nil {
			return days, events, e
		}
		if !found {
			return days, events, nil // caught up
		}
		dayEnd := day.AddDate(0, 0, 1)
		n, e := s.archiveDay(ctx, day, dayEnd)
		events += n
		if e != nil {
			return days, events, e
		}
		days++
	}
	logger.L().Warn("archive hit per-run day cap, remaining days deferred to next run", zap.Int("cap", maxDaysPerRun))
	return days, events, nil
}

// archiveDay admits a fixed identity range and publishes complete snapshots in
// bounded batches. Each verified batch commits its own exact deletion set.
func (s *ArchiveScheduler) archiveDay(ctx context.Context, start, end time.Time) (int64, error) {
	upper, err := s.eventRepo.ArchiveDayUpperID(ctx, start, end)
	if err != nil {
		return 0, err
	}
	var after, deleted int64
	for after < upper {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		snapshot, err := s.eventRepo.ReadArchiveBatch(ctx, start, end, after, upper, s.batchSize)
		if err != nil {
			return deleted, err
		}
		if len(snapshot.Events) == 0 {
			break
		}
		n, err := s.archiveBatch(ctx, start, snapshot)
		deleted += n
		if err != nil {
			return deleted, err
		}
		after = snapshot.Events[len(snapshot.Events)-1].ID
		if s.batchSleep > 0 {
			timer := time.NewTimer(s.batchSleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return deleted, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return deleted, nil
}

type archiveObject struct {
	Key    string           `json:"key"`
	Digest coldstore.Digest `json:"digest"`
	Rows   int              `json:"rows"`
}
type archiveBatchManifest struct {
	SchemaVersion  int             `json:"schema_version"`
	Day            string          `json:"day"`
	SnapshotSHA256 string          `json:"snapshot_sha256"`
	EventIDs       []int64         `json:"event_ids"`
	Objects        []archiveObject `json:"objects"`
}
type archiveFile struct {
	path   string
	digest coldstore.Digest
}

func bytesDigest(body []byte) coldstore.Digest {
	hash := sha256.Sum256(body)
	return coldstore.Digest{Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:])}
}

func (s *ArchiveScheduler) archiveBatch(ctx context.Context, day time.Time, snapshot repository.ArchiveSnapshot) (int64, error) {
	body, err := sonic.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	if len(body) > 64<<20 {
		return 0, errors.New("archive_batch_too_large")
	}
	snapshotHash := bytesDigest(body).SHA256
	date := day.Format("2006-01-02")
	base := s.prefix + "event/dt=" + date + "/batch=" + snapshotHash + "/"
	manifest := archiveBatchManifest{SchemaVersion: 2, Day: date, SnapshotSHA256: snapshotHash, EventIDs: make([]int64, len(snapshot.Events)), Objects: []archiveObject{}}
	for i, event := range snapshot.Events {
		manifest.EventIDs[i] = event.ID
	}
	files := []archiveFile{}
	defer func() {
		for _, file := range files {
			_ = os.Remove(file.path)
		}
	}()
	events, err := writeArchiveRows(ctx, snapshot.Events)
	if err != nil {
		return 0, err
	}
	files = append(files, events)
	traces, err := writeArchiveRows(ctx, snapshot.Traces)
	if err != nil {
		return 0, err
	}
	files = append(files, traces)
	results, err := writeArchiveRows(ctx, snapshot.AIResults)
	if err != nil {
		return 0, err
	}
	files = append(files, results)
	names := []string{"pipeline_event", "pipeline_event_trace", "pipeline_ai_result"}
	rows := []int{len(snapshot.Events), len(snapshot.Traces), len(snapshot.AIResults)}
	for i, file := range files {
		key := base + names[i] + "-" + file.digest.SHA256 + ".jsonl.gz"
		if err := s.putArchiveFile(ctx, key, file); err != nil {
			return 0, err
		}
		manifest.Objects = append(manifest.Objects, archiveObject{Key: key, Digest: file.digest, Rows: rows[i]})
	}
	manifestBody, err := sonic.Marshal(manifest)
	if err != nil {
		return 0, err
	}
	manifestDigest := bytesDigest(manifestBody)
	manifestKey := base + "_manifest-" + manifestDigest.SHA256 + ".json"
	if err := s.cold.PutImmutable(ctx, manifestKey, bytes.NewReader(manifestBody), manifestDigest); err != nil {
		return 0, err
	}
	// Verify every published object, including the manifest, before entering the
	// deletion transaction. A successful PUT or an existing key is not proof.
	for _, object := range manifest.Objects {
		if err := s.cold.Verify(ctx, object.Key, object.Digest); err != nil {
			return 0, err
		}
	}
	if err := s.cold.Verify(ctx, manifestKey, manifestDigest); err != nil {
		return 0, err
	}
	deleted, err := s.eventRepo.DeleteArchiveSnapshot(ctx, snapshot)
	if err == nil {
		logger.L().Info("archive batch committed", zap.String("day", date), zap.String("snapshot", snapshotHash), zap.Int64("events", deleted))
	}
	return deleted, err
}
func (s *ArchiveScheduler) putArchiveFile(ctx context.Context, key string, file archiveFile) error {
	f, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.cold.PutImmutable(ctx, key, f, file.digest)
}

// Gzip's zero timestamp and the ordered snapshot rows keep bytes stable across
// retries. Publishing a new batch cannot replace an earlier partial run's data.
func writeArchiveRows[T any](ctx context.Context, rows []T) (result archiveFile, err error) {
	f, err := os.CreateTemp("", "q4d-archive-*.jsonl.gz")
	if err != nil {
		return result, err
	}
	result.path = f.Name()
	succeeded := false
	defer func() {
		_ = f.Close()
		if !succeeded {
			_ = os.Remove(result.path)
		}
	}()
	gz := gzip.NewWriter(f)
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			_ = gz.Close()
			return result, err
		}
		var body []byte
		body, err = sonic.Marshal(row)
		if err != nil {
			_ = gz.Close()
			return result, err
		}
		if _, err = gz.Write(append(body, '\n')); err != nil {
			_ = gz.Close()
			return result, err
		}
	}
	if err = gz.Close(); err != nil {
		return result, err
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	hash := sha256.New()
	result.digest.Size, err = io.Copy(hash, f)
	if err != nil {
		return result, err
	}
	result.digest.SHA256 = hex.EncodeToString(hash.Sum(nil))
	succeeded = true
	return result, nil
}
