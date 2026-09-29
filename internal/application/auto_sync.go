package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// AutoSyncStatus is what the UI sees: whether it is on, what time it runs, the next and
// last trigger times, and the task id the last run produced.
type AutoSyncStatus struct {
	Enabled    bool       `json:"enabled"`
	DailyTime  string     `json:"daily_time"`
	NextRunAt  *time.Time `json:"next_run_at"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastTaskID int64      `json:"last_task_id"`
	LastError  string     `json:"last_error,omitempty"`
}

// AutoSyncScheduler triggers one mode=incremental, period=1d sync per day at the
// configured local time. It goes through DataSyncApp.Enqueue, so the result also shows
// up under sync history.
type AutoSyncScheduler struct {
	app       *DataSyncApp
	enabled   bool
	hour, min int

	mu         sync.Mutex
	lastRunAt  *time.Time
	lastTaskID int64
	lastErr    string
	nextRunAt  *time.Time
	changed    chan struct{}
	revision   uint64
}

func NewAutoSyncScheduler(app *DataSyncApp, enabled bool, dailyTime string) (*AutoSyncScheduler, error) {
	h, m, err := parseDailyTime(dailyTime)
	if err != nil {
		return nil, err
	}
	return &AutoSyncScheduler{app: app, enabled: enabled, hour: h, min: m, changed: make(chan struct{}, 1)}, nil
}

// Update changes the next scheduled run without altering in-flight sync tasks.
func (s *AutoSyncScheduler) Update(enabled bool, dailyTime string) error {
	h, m, err := parseDailyTime(dailyTime)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.enabled, s.hour, s.min = enabled, h, m
	s.revision++
	s.nextRunAt = nil
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return nil
}

func parseDailyTime(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 3, 0, nil
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("auto_sync.daily_time must be HH:MM, got %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("auto_sync.daily_time hour invalid: %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("auto_sync.daily_time minute invalid: %q", s)
	}
	return h, m, nil
}

// Status is for the handler.
func (s *AutoSyncScheduler) Status() AutoSyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AutoSyncStatus{
		Enabled:    s.enabled,
		DailyTime:  fmt.Sprintf("%02d:%02d", s.hour, s.min),
		NextRunAt:  s.nextRunAt,
		LastRunAt:  s.lastRunAt,
		LastTaskID: s.lastTaskID,
		LastError:  s.lastErr,
	}
}

// Start launches a goroutine that sleeps until the next daily_time and then triggers;
// it returns a cancel for shutdown. Disabled schedulers wait for a settings update.
func (s *AutoSyncScheduler) Start() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go s.loop(ctx)
	return cancel
}

func (s *AutoSyncScheduler) loop(ctx context.Context) {
	for {
		s.mu.Lock()
		enabled, revision := s.enabled, s.revision
		var next time.Time
		if enabled {
			next = s.computeNextLocked(time.Now())
			s.nextRunAt = &next
		} else {
			s.nextRunAt = nil
		}
		s.mu.Unlock()
		if !enabled {
			select {
			case <-ctx.Done():
				return
			case <-s.changed:
				continue
			}
		}

		wait := time.Until(next)
		logger.L().Info("auto sync scheduled",
			zap.Time("next_run_at", next), zap.Duration("wait", wait))

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.changed:
			timer.Stop()
			continue
		case <-timer.C:
			s.triggerRevision(ctx, revision)
		}
	}
}

func (s *AutoSyncScheduler) computeNext(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.computeNextLocked(now)
}

func (s *AutoSyncScheduler) computeNextLocked(now time.Time) time.Time {
	loc := now.Location()
	candidate := time.Date(now.Year(), now.Month(), now.Day(), s.hour, s.min, 0, 0, loc)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func (s *AutoSyncScheduler) trigger(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triggerLocked(ctx)
}

func (s *AutoSyncScheduler) triggerRevision(ctx context.Context, revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled && s.revision == revision {
		s.triggerLocked(ctx)
	}
}

// Admission and configuration replacement share the same gate. When disabling
// returns, an already-fired timer cannot enqueue a new task afterwards.
func (s *AutoSyncScheduler) triggerLocked(ctx context.Context) {
	task := &domain.DataSyncTask{
		Mode:   domain.DataSyncModeIncremental,
		Period: domain.Bar1d,
	}
	out, err := s.app.Enqueue(ctx, task)
	now := time.Now()
	s.lastRunAt = &now
	if err != nil {
		s.lastErr = err.Error()
		logger.L().Error("auto sync enqueue failed", zap.Error(err))
	} else {
		s.lastErr = ""
		if out != nil {
			s.lastTaskID = out.ID
		}
		logger.L().Info("auto sync enqueued", zap.Int64("task_id", s.lastTaskID))
	}
}
