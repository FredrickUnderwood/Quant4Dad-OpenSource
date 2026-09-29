package repository

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

var ErrEventResultState = errors.New("event_result_state_conflict")

type EventRepository struct {
	db *gorm.DB
}

func NewEventRepository(db *gorm.DB) *EventRepository {
	return &EventRepository{db: db}
}

func (r *EventRepository) Create(ctx context.Context, e *domain.Event) error {
	if err := r.db.WithContext(ctx).Create(e).Error; err != nil {
		logger.L().Error("event create failed", zap.String("uid", e.EventUID), zap.Error(err))
		return err
	}
	return nil
}

// SaveResult writes the event's final state, the node lineage and the AI results inside one
// transaction.
func (r *EventRepository) SaveResult(ctx context.Context, e *domain.Event, traces []domain.EventTrace, aiResults []domain.AIResult) error {
	if e == nil || !isTerminalEvent(e.Status) {
		return ErrEventResultState
	}
	for _, row := range traces {
		if row.EventID != e.ID {
			return ErrEventResultState
		}
	}
	for _, row := range aiResults {
		if row.EventID != e.ID {
			return ErrEventResultState
		}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&domain.Event{}).Where("id = ? AND status = ?", e.ID, domain.EventStatusProcessing).Updates(map[string]any{
			"final_payload":   e.FinalPayload,
			"status":          e.Status,
			"dropped_at_node": e.DroppedAtNode,
			"error":           e.Error,
			"finished_at":     e.FinishedAt,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrEventResultState
		}
		if len(traces) > 0 {
			if err := tx.Create(&traces).Error; err != nil {
				return err
			}
		}
		if len(aiResults) > 0 {
			if err := tx.Create(&aiResults).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		logger.L().Error("event result save failed", zap.Int64("id", e.ID), zap.Error(err))
	}
	return err
}

type EventQuery struct {
	PipelineID int64
	Status     string
	// Start and End filter on the event's ingestion time, received_at, over the half-open
	// interval [Start, End). A zero value leaves that side unbounded.
	Start  time.Time
	End    time.Time
	Limit  int
	Offset int
}

func (r *EventRepository) List(ctx context.Context, q EventQuery) ([]*domain.Event, int64, error) {
	tx := r.db.WithContext(ctx).Model(&domain.Event{})
	if q.PipelineID > 0 {
		tx = tx.Where("pipeline_id = ?", q.PipelineID)
	}
	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if !q.Start.IsZero() {
		tx = tx.Where("received_at >= ?", q.Start)
	}
	if !q.End.IsZero() {
		tx = tx.Where("received_at < ?", q.End)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var items []*domain.Event
	if err := tx.Order("id DESC").Limit(limit).Offset(q.Offset).Find(&items).Error; err != nil {
		logger.L().Error("event list failed", zap.Error(err))
		return nil, 0, err
	}
	return items, total, nil
}

// exportMaxRows caps how many event rows one export fetches, so an unfiltered export cannot
// pull the whole table into memory.
const exportMaxRows = 50000

// ListAll fetches every matching event without paging, for export. Results come back in
// descending id order, with exportMaxRows as a hard backstop so exporting a large table
// cannot exhaust memory.
func (r *EventRepository) ListAll(ctx context.Context, q EventQuery) ([]*domain.Event, error) {
	tx := r.db.WithContext(ctx).Model(&domain.Event{})
	if q.PipelineID > 0 {
		tx = tx.Where("pipeline_id = ?", q.PipelineID)
	}
	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if !q.Start.IsZero() {
		tx = tx.Where("received_at >= ?", q.Start)
	}
	if !q.End.IsZero() {
		tx = tx.Where("received_at < ?", q.End)
	}
	var items []*domain.Event
	if err := tx.Order("id DESC").Limit(exportMaxRows).Find(&items).Error; err != nil {
		logger.L().Error("event export query failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// GetByID loads an event along with its lineage and AI results.
func (r *EventRepository) GetByID(ctx context.Context, id int64) (*domain.Event, error) {
	var e domain.Event
	if err := r.db.WithContext(ctx).First(&e, id).Error; err != nil {
		logger.L().Error("event get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	if err := r.db.WithContext(ctx).Where("event_id = ?", id).Order("id ASC").Find(&e.Traces).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Where("event_id = ?", id).Order("id ASC").Find(&e.AIResults).Error; err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EventRepository) ListAIResults(ctx context.Context, eventID int64) ([]*domain.AIResult, error) {
	var items []*domain.AIResult
	if err := r.db.WithContext(ctx).Where("event_id = ?", eventID).Order("id ASC").Find(&items).Error; err != nil {
		logger.L().Error("ai result list failed", zap.Int64("event_id", eventID), zap.Error(err))
		return nil, err
	}
	return items, nil
}
