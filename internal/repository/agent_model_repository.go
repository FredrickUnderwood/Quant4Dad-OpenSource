package repository

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// LLMProviderSnapshot reads model configuration, revision and probe facts in one
// database statement. All values are JSON text and must not be logged.
type LLMProviderSnapshot struct {
	Value    []byte
	Revision []byte
	Probes   []byte
}

func (r *SettingRepository) GetLLMProviderSnapshot(ctx context.Context) (LLMProviderSnapshot, error) {
	return readLLMProviderSnapshot(r.db.WithContext(ctx))
}

func readLLMProviderSnapshot(db *gorm.DB) (LLMProviderSnapshot, error) {
	var rows []domain.Setting
	if err := db.Where("`key` IN ?", []string{domain.SettingKeyLLMProviders, domain.SettingKeyAgentModelRevision, domain.SettingKeyAgentModelProbes}).Find(&rows).Error; err != nil {
		return LLMProviderSnapshot{}, err
	}
	out := LLMProviderSnapshot{Value: []byte(`{}`)}
	for _, row := range rows {
		switch row.Key {
		case domain.SettingKeyLLMProviders:
			out.Value = row.Value
		case domain.SettingKeyAgentModelRevision:
			out.Revision = row.Value
		case domain.SettingKeyAgentModelProbes:
			out.Probes = row.Value
		}
	}
	return out, nil
}

// UpdateLLMProviders serializes read/merge/write across processes. The write
// before the read takes SQLite's writer lock and MySQL's row lock even on first
// creation. The callback must do no external I/O; all rows commit or roll back.
func (r *SettingRepository) UpdateLLMProviders(ctx context.Context, update func(LLMProviderSnapshot) (LLMProviderSnapshot, error)) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seed := domain.Setting{Key: domain.SettingKeyLLMProviders, Value: []byte(`{}`)}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE setting SET `key` = `key` WHERE `key` = ?", domain.SettingKeyLLMProviders).Error; err != nil {
			return err
		}
		before, err := readLLMProviderSnapshot(tx)
		if err != nil {
			return err
		}
		after, err := update(before)
		if err != nil {
			return err
		}
		rows := []domain.Setting{
			{Key: domain.SettingKeyLLMProviders, Value: after.Value},
			{Key: domain.SettingKeyAgentModelRevision, Value: after.Revision},
		}
		if after.Probes != nil {
			rows = append(rows, domain.Setting{Key: domain.SettingKeyAgentModelProbes, Value: after.Probes})
		}
		for _, row := range rows {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Driver errors may include the entire stored provider JSON.
		logger.L().Error("model settings transaction failed", zap.String("error_type", fmt.Sprintf("%T", err)))
	}
	return err
}
