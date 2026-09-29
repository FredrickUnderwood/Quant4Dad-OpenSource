package repository

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

var ErrArchiveSnapshotChanged = errors.New("archive_snapshot_changed")
var archiveTerminal = []string{domain.EventStatusPassed, domain.EventStatusDropped, domain.EventStatusFailed}

func isTerminalEvent(status string) bool {
	return status == domain.EventStatusPassed || status == domain.EventStatusDropped || status == domain.EventStatusFailed
}

// ArchiveSnapshot is a bounded, consistent set of complete terminal events.
// It contains event domain rows, without adding live connection configuration.
type ArchiveSnapshot struct {
	Events    []*domain.Event     `json:"events"`
	Traces    []domain.EventTrace `json:"traces"`
	AIResults []domain.AIResult   `json:"ai_results"`
}

func emptyArchiveSnapshot() ArchiveSnapshot {
	return ArchiveSnapshot{Events: []*domain.Event{}, Traces: []domain.EventTrace{}, AIResults: []domain.AIResult{}}
}
func (r *EventRepository) OldestEventDayBefore(ctx context.Context, before time.Time) (day time.Time, found bool, err error) {
	var e domain.Event
	err = r.db.WithContext(ctx).Where("received_at < ? AND status IN ?", before, archiveTerminal).Order("received_at ASC").Limit(1).Find(&e).Error
	if err != nil || e.ID == 0 {
		return time.Time{}, false, err
	}
	t := e.ReceivedAt.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()), true, nil
}

// ArchiveDayUpperID fixes the day's admitted identity range. Later inserts are
// left for a future round and cannot enter a previously exported deletion set.
func (r *EventRepository) ArchiveDayUpperID(ctx context.Context, start, end time.Time) (int64, error) {
	var row domain.Event
	err := r.db.WithContext(ctx).Where("received_at >= ? AND received_at < ? AND status IN ?", start, end, archiveTerminal).Order("id DESC").Limit(1).Find(&row).Error
	return row.ID, err
}
func loadArchiveChildren(tx *gorm.DB, snapshot *ArchiveSnapshot, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("event_id IN ?", ids).Order("id ASC").Find(&snapshot.Traces).Error; err != nil {
		return err
	}
	return tx.Where("event_id IN ?", ids).Order("id ASC").Find(&snapshot.AIResults).Error
}
func (r *EventRepository) ReadArchiveBatch(ctx context.Context, start, end time.Time, afterID, upperID int64, limit int) (ArchiveSnapshot, error) {
	snapshot := emptyArchiveSnapshot()
	if limit < 1 || limit > 2000 {
		limit = 2000
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("received_at >= ? AND received_at < ? AND id > ? AND id <= ? AND status IN ?", start, end, afterID, upperID, archiveTerminal).Order("id ASC").Limit(limit).Find(&snapshot.Events).Error; err != nil {
			return err
		}
		ids := make([]int64, len(snapshot.Events))
		for i, event := range snapshot.Events {
			ids[i] = event.ID
		}
		return loadArchiveChildren(tx, &snapshot, ids)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return snapshot, err
}

// DeleteArchiveSnapshot never discovers rows by date. It obtains write locks
// first, checks the complete remaining snapshot, and deletes exactly those IDs
// in one short transaction. Concurrent successful deletion is an idempotent no-op.
func (r *EventRepository) DeleteArchiveSnapshot(ctx context.Context, expected ArchiveSnapshot) (int64, error) {
	if len(expected.Events) == 0 {
		return 0, nil
	}
	if len(expected.Events) > 2000 {
		return 0, ErrArchiveSnapshotChanged
	}
	ids := make([]int64, len(expected.Events))
	seen := map[int64]bool{}
	for i, event := range expected.Events {
		if event == nil || event.ID <= 0 || seen[event.ID] || !isTerminalEvent(event.Status) {
			return 0, ErrArchiveSnapshotChanged
		}
		ids[i] = event.ID
		seen[event.ID] = true
	}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Same portable lock-before-read pattern used by model-setting mutations.
		// SQLite obtains its writer lock; MySQL locks these parent rows.
		if err := tx.Exec("UPDATE pipeline_event SET id = id WHERE id IN ?", ids).Error; err != nil {
			return err
		}
		current := emptyArchiveSnapshot()
		if err := tx.Where("id IN ?", ids).Order("id ASC").Find(&current.Events).Error; err != nil {
			return err
		}
		if err := loadArchiveChildren(tx, &current, ids); err != nil {
			return err
		}
		present := map[int64]bool{}
		for _, e := range current.Events {
			present[e.ID] = true
		}
		remaining := emptyArchiveSnapshot()
		for _, e := range expected.Events {
			if present[e.ID] {
				remaining.Events = append(remaining.Events, e)
			}
		}
		for _, row := range expected.Traces {
			if present[row.EventID] {
				remaining.Traces = append(remaining.Traces, row)
			}
		}
		for _, row := range expected.AIResults {
			if present[row.EventID] {
				remaining.AIResults = append(remaining.AIResults, row)
			}
		}
		if !reflect.DeepEqual(current, remaining) {
			return ErrArchiveSnapshotChanged
		}
		if len(current.Events) == 0 {
			return nil
		}
		if err := tx.Where("event_id IN ?", ids).Delete(&domain.AIResult{}).Error; err != nil {
			return err
		}
		if err := tx.Where("event_id IN ?", ids).Delete(&domain.EventTrace{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", ids).Delete(&domain.Event{})
		deleted = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
