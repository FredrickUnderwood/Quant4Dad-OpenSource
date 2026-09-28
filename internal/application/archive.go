// Package application: archive.go is the hot/cold tiering scheduler for the three event
// tables. At the scheduled time each day it exports whole days of events whose
// received_at falls outside the retention window — along with their traces and
// ai_results — to object storage as jsonl.gz, and only after verifying the upload does
// it delete them from MySQL in throttled batches, leaving the live database carrying
// hot data only.
package application

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

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
		if e != nil {
			return days, events, fmt.Errorf("archive day %s: %w", day.Format("2006-01-02"), e)
		}
		days++
		events += n
	}
	logger.L().Warn("archive hit per-run day cap, remaining days deferred to next run", zap.Int("cap", maxDaysPerRun))
	return days, events, nil
}

// archiveDay archives the events in one day, [dayStart, dayEnd): it first exports all
// three tables to the cold store and verifies them, and only once that has succeeded does
// it delete in throttled batches. It returns how many events that day had deleted.
func (s *ArchiveScheduler) archiveDay(ctx context.Context, dayStart, dayEnd time.Time) (int64, error) {
	dt := dayStart.Format("2006-01-02")
	base := s.prefix + "event/dt=" + dt + "/"

	// 1) Export to a local temporary gz and then upload, rather than holding a whole day
	// of data in memory.
	exp, cleanup, err := s.exportDay(ctx, dayStart, dayEnd)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return 0, err
	}
	if exp.events == 0 {
		// Shouldn't happen — OldestEventDayBefore already confirmed there is data — but
		// defend against it anyway.
		logger.L().Warn("archive day has no events, skip", zap.String("dt", dt))
		return 0, nil
	}

	// 2) Upload the three objects plus the manifest, then Head each one to verify it exists.
	objects := []struct {
		key, path string
	}{
		{base + "pipeline_event.jsonl.gz", exp.eventPath},
		{base + "pipeline_event_trace.jsonl.gz", exp.tracePath},
		{base + "pipeline_ai_result.jsonl.gz", exp.aiPath},
	}
	for _, o := range objects {
		if err := s.putFile(ctx, o.key, o.path); err != nil {
			return 0, err
		}
	}
	manifest, _ := json.Marshal(map[string]any{
		"day":         dt,
		"events":      exp.events,
		"traces":      exp.traces,
		"ai_results":  exp.aiResults,
		"archived_at": time.Now().Format(time.RFC3339),
	})
	if err := s.putBytes(ctx, base+"_manifest.json", manifest); err != nil {
		return 0, err
	}
	for _, o := range objects {
		ok, err := s.cold.Exists(ctx, o.key)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("archive verify failed, object missing after upload: %s", o.key)
		}
	}
	logger.L().Info("archive day uploaded",
		zap.String("dt", dt), zap.Int64("events", exp.events),
		zap.Int64("traces", exp.traces), zap.Int64("ai_results", exp.aiResults))

	// 3) Only delete once verification passes: repeatedly take that day's remaining events,
	// delete from all three tables by event_id, and sleep between batches to throttle.
	var deleted int64
	for {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}
		batch, err := s.eventRepo.ScanEventsByDay(ctx, dayStart, dayEnd, 0, s.batchSize)
		if err != nil {
			return deleted, err
		}
		if len(batch) == 0 {
			break
		}
		ids := make([]int64, len(batch))
		for i, e := range batch {
			ids[i] = e.ID
		}
		n, err := s.eventRepo.DeleteArchivedBatch(ctx, ids)
		if err != nil {
			return deleted, err
		}
		deleted += n
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
	logger.L().Info("archive day deleted from mysql", zap.String("dt", dt), zap.Int64("events", deleted))
	return deleted, nil
}

// dayExport holds the temporary file paths and row counts from exporting one day.
type dayExport struct {
	eventPath, tracePath, aiPath string
	events, traces, aiResults    int64
}

// exportDay streams one day's events, traces and AI results into three separate
// temporary gz files. Events are the anchor, with traces and ai_results following by
// event_id, which keeps the three tables consistent and free of orphans. cleanup removes
// the temporary files.
func (s *ArchiveScheduler) exportDay(ctx context.Context, dayStart, dayEnd time.Time) (exp dayExport, cleanup func(), err error) {
	ew, ePath, err := newGzTemp("q4d-arch-event-*.jsonl.gz")
	if err != nil {
		return exp, nil, err
	}
	tw, tPath, err := newGzTemp("q4d-arch-trace-*.jsonl.gz")
	if err != nil {
		ew.close()
		return exp, nil, err
	}
	aw, aPath, err := newGzTemp("q4d-arch-ai-*.jsonl.gz")
	if err != nil {
		ew.close()
		tw.close()
		return exp, nil, err
	}
	cleanup = func() {
		_ = os.Remove(ePath)
		_ = os.Remove(tPath)
		_ = os.Remove(aPath)
	}
	exp.eventPath, exp.tracePath, exp.aiPath = ePath, tPath, aPath

	var afterID int64
	for {
		select {
		case <-ctx.Done():
			ew.close()
			tw.close()
			aw.close()
			return exp, cleanup, ctx.Err()
		default:
		}
		batch, e := s.eventRepo.ScanEventsByDay(ctx, dayStart, dayEnd, afterID, s.batchSize)
		if e != nil {
			ew.close()
			tw.close()
			aw.close()
			return exp, cleanup, e
		}
		if len(batch) == 0 {
			break
		}
		ids := make([]int64, len(batch))
		for i, ev := range batch {
			ids[i] = ev.ID
			if e := ew.enc.Encode(ev); e != nil {
				ew.close()
				tw.close()
				aw.close()
				return exp, cleanup, e
			}
			exp.events++
		}
		afterID = batch[len(batch)-1].ID

		traces, e := s.eventRepo.ListTracesByEventIDs(ctx, ids)
		if e != nil {
			ew.close()
			tw.close()
			aw.close()
			return exp, cleanup, e
		}
		for i := range traces {
			if e := tw.enc.Encode(&traces[i]); e != nil {
				ew.close()
				tw.close()
				aw.close()
				return exp, cleanup, e
			}
			exp.traces++
		}

		ais, e := s.eventRepo.ListAIResultsByEventIDs(ctx, ids)
		if e != nil {
			ew.close()
			tw.close()
			aw.close()
			return exp, cleanup, e
		}
		for i := range ais {
			if e := aw.enc.Encode(&ais[i]); e != nil {
				ew.close()
				tw.close()
				aw.close()
				return exp, cleanup, e
			}
			exp.aiResults++
		}
	}

	// Close and flush; any failure counts as a failed export, and nothing may be deleted
	// on the strength of it.
	if e := ew.close(); e != nil {
		tw.close()
		aw.close()
		return exp, cleanup, e
	}
	if e := tw.close(); e != nil {
		aw.close()
		return exp, cleanup, e
	}
	if e := aw.close(); e != nil {
		return exp, cleanup, e
	}
	return exp, cleanup, nil
}

func (s *ArchiveScheduler) putFile(ctx context.Context, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.cold.Put(ctx, key, f)
}

func (s *ArchiveScheduler) putBytes(ctx context.Context, key string, b []byte) error {
	return s.cold.Put(ctx, key, bytes.NewReader(b))
}

// gzTemp is a temporary gz file plus the JSON-lines encoder writing to it.
type gzTemp struct {
	f   *os.File
	gz  *gzip.Writer
	enc *json.Encoder
}

func newGzTemp(pattern string) (*gzTemp, string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, "", err
	}
	gz := gzip.NewWriter(f)
	return &gzTemp{f: f, gz: gz, enc: json.NewEncoder(gz)}, f.Name(), nil
}

// close flushes and closes both the gzip writer and the file. Safe to call repeatedly:
// every call after the first is a no-op.
func (g *gzTemp) close() error {
	if g.gz == nil {
		return nil
	}
	gzErr := g.gz.Close()
	g.gz = nil
	syncErr := g.f.Sync()
	closeErr := g.f.Close()
	if gzErr != nil {
		return gzErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
