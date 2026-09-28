package repository

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// This file is EventRepository's archiving support: it lets application.ArchiveScheduler
// export expired events — along with their traces and ai_results — to the cold store a day
// at a time, then delete them in batches. pipeline_event is the anchor, with traces and
// ai_results always following by event_id, which keeps orphans from appearing.

// OldestEventDayBefore returns local midnight of the day containing the earliest event whose
// received_at is before `before`. found=false means there is no such event, so archiving has
// caught up and this round has nothing to do.
func (r *EventRepository) OldestEventDayBefore(ctx context.Context, before time.Time) (day time.Time, found bool, err error) {
	// Read the oldest row's received_at rather than using a MIN aggregate: under sqlite the
	// aggregate comes back as a string that will not Scan straight into a time.Time, whereas
	// reading a single row by column lets the driver convert it correctly on both mysql and
	// sqlite.
	var e domain.Event
	err = r.db.WithContext(ctx).
		Where("received_at < ?", before).
		Order("received_at ASC").Limit(1).Find(&e).Error
	if err != nil {
		logger.L().Error("archive: query oldest event day failed", zap.Error(err))
		return time.Time{}, false, err
	}
	if e.ID == 0 {
		return time.Time{}, false, nil
	}
	t := e.ReceivedAt.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()), true, nil
}

// ScanEventsByDay walks one day's events, [dayStart, dayEnd), with an ascending-id cursor.
// Pass the last id of the previous batch as afterID, or 0 for the first batch. An empty
// slice means the day has been scanned through.
func (r *EventRepository) ScanEventsByDay(ctx context.Context, dayStart, dayEnd time.Time, afterID int64, limit int) ([]*domain.Event, error) {
	if limit <= 0 {
		limit = 2000
	}
	var items []*domain.Event
	if err := r.db.WithContext(ctx).
		Where("received_at >= ? AND received_at < ? AND id > ?", dayStart, dayEnd, afterID).
		Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		logger.L().Error("archive: scan events by day failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// ListTracesByEventIDs fetches every lineage row for a batch of events, for the archive
// export.
func (r *EventRepository) ListTracesByEventIDs(ctx context.Context, eventIDs []int64) ([]domain.EventTrace, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	var items []domain.EventTrace
	if err := r.db.WithContext(ctx).
		Where("event_id IN ?", eventIDs).Order("id ASC").Find(&items).Error; err != nil {
		logger.L().Error("archive: list traces by event ids failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// ListAIResultsByEventIDs fetches every AI result for a batch of events, for the archive
// export.
func (r *EventRepository) ListAIResultsByEventIDs(ctx context.Context, eventIDs []int64) ([]domain.AIResult, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	var items []domain.AIResult
	if err := r.db.WithContext(ctx).
		Where("event_id IN ?", eventIDs).Order("id ASC").Find(&items).Error; err != nil {
		logger.L().Error("archive: list ai results by event ids failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// DeleteArchivedBatch deletes a batch of events along with their traces and ai_results in
// one transaction, children before parents. Call it only once the corresponding data is
// confirmed uploaded to the cold store. It returns how many event rows were deleted.
func (r *EventRepository) DeleteArchivedBatch(ctx context.Context, eventIDs []int64) (int64, error) {
	if len(eventIDs) == 0 {
		return 0, nil
	}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("event_id IN ?", eventIDs).Delete(&domain.AIResult{}).Error; err != nil {
			return err
		}
		if err := tx.Where("event_id IN ?", eventIDs).Delete(&domain.EventTrace{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", eventIDs).Delete(&domain.Event{})
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected
		return nil
	})
	if err != nil {
		logger.L().Error("archive: delete archived batch failed", zap.Int("n", len(eventIDs)), zap.Error(err))
		return 0, err
	}
	return deleted, nil
}
