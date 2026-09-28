package repository

import (
	"context"

	"github.com/quant4dad/internal/domain"
)

func (r *AgentSessionRepository) DueTitles(ctx context.Context, now int64) ([]domain.AgentSessionBinding, error) {
	rows := []domain.AgentSessionBinding{}
	err := r.db.WithContext(ctx).Where("title_generation > title_settled_generation AND title_lease_until_ms <= ? AND status IN ?", now,
		[]string{domain.AgentSessionActive, domain.AgentSessionArchived}).Order("updated_at ASC").Limit(8).Find(&rows).Error
	return rows, sessionStoreError(err)
}

func (r *AgentSessionRepository) ClaimTitle(ctx context.Context, row domain.AgentSessionBinding, attempt string, now int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&domain.AgentSessionBinding{}).
		Where("actor_id = ? AND id = ? AND title_generation = ? AND title_generation > title_settled_generation AND title_lease_until_ms <= ?",
			row.ActorID, row.ID, row.TitleGeneration, now).
		Updates(map[string]any{"title_attempt": attempt, "title_lease_until_ms": now + 45000})
	return result.RowsAffected == 1, sessionStoreError(result.Error)
}

// An empty title settles an unsuccessful attempt without replacing the title.
// A newer message, manual rename or another worker's lease invalidates the CAS.
func (r *AgentSessionRepository) FinishTitle(ctx context.Context, row domain.AgentSessionBinding, attempt, title string) (bool, error) {
	values := map[string]any{"title_settled_generation": row.TitleGeneration, "title_attempt": "", "title_lease_until_ms": 0}
	if title != "" {
		values["title"] = title
	}
	result := r.db.WithContext(ctx).Model(&domain.AgentSessionBinding{}).
		Where("actor_id = ? AND id = ? AND title_generation = ? AND title_settled_generation < ? AND title_attempt = ?",
			row.ActorID, row.ID, row.TitleGeneration, row.TitleGeneration, attempt).Updates(values)
	return result.RowsAffected == 1, sessionStoreError(result.Error)
}
