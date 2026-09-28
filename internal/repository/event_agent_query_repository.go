package repository

import (
	"context"
	"errors"
	"github.com/quant4dad/internal/domain"
)

func (r *EventRepository) ListAgent(ctx context.Context, q EventQuery) ([]*domain.Event, int64, error) {
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 10000 {
		return nil, 0, errors.New("event_query_limit_invalid")
	}
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
	var count int64
	if err := tx.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	var items []*domain.Event
	err := tx.Select("id,event_uid,pipeline_id,source,status,received_at,finished_at,CASE WHEN LENGTH(final_payload)>512 THEN ? ELSE final_payload END AS final_payload", `{"omitted":true}`).Order("id DESC").Limit(q.Limit).Offset(q.Offset).Find(&items).Error
	return items, count, err
}
