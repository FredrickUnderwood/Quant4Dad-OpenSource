package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

func modelRepositoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "model.db") + "?_busy_timeout=5000"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Setting{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return db
}

func TestModelSettingsTransactionRollback(t *testing.T) {
	db := modelRepositoryDB(t)
	repo := NewSettingRepository(db)
	ctx := context.Background()
	before := LLMProviderSnapshot{Value: []byte(`{"old":true}`), Revision: []byte(`"before"`)}
	if err := repo.UpdateLLMProviders(ctx, func(LLMProviderSnapshot) (LLMProviderSnapshot, error) { return before, nil }); err != nil {
		t.Fatal(err)
	}
	// Fail the second write after the provider row has already changed.
	if err := db.Exec("CREATE TRIGGER reject_model_revision BEFORE INSERT ON setting WHEN NEW.key = 'agent.model.revision' BEGIN SELECT RAISE(ABORT, 'revision rejected'); END").Error; err != nil {
		t.Fatal(err)
	}
	err := repo.UpdateLLMProviders(ctx, func(LLMProviderSnapshot) (LLMProviderSnapshot, error) {
		return LLMProviderSnapshot{Value: []byte(`{"new":true}`), Revision: []byte(`"after"`)}, nil
	})
	if err == nil {
		t.Fatal("expected injected database failure")
	}
	after, err := repo.GetLLMProviderSnapshot(ctx)
	if err != nil || !bytes.Equal(after.Value, before.Value) || !bytes.Equal(after.Revision, before.Revision) {
		t.Fatal("partial write escaped rollback")
	}
}

func assertModelSettingsConcurrency(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	repo := NewSettingRepository(db)
	if err := repo.UpdateLLMProviders(ctx, func(LLMProviderSnapshot) (LLMProviderSnapshot, error) {
		return LLMProviderSnapshot{Value: []byte("0"), Revision: []byte("0")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	const writers = 12
	var wg sync.WaitGroup
	results := make(chan error, writers+1)
	wg.Add(writers + 1)
	for range writers {
		go func() {
			defer wg.Done()
			// Separate repository instances exercise database locking, not a Go mutex.
			results <- NewSettingRepository(db).UpdateLLMProviders(ctx, func(old LLMProviderSnapshot) (LLMProviderSnapshot, error) {
				n, err := strconv.Atoi(string(old.Value))
				v := []byte(strconv.Itoa(n + 1))
				return LLMProviderSnapshot{Value: v, Revision: v}, err
			})
		}()
	}
	go func() {
		defer wg.Done()
		for range 100 {
			pair, err := repo.GetLLMProviderSnapshot(ctx)
			if err != nil {
				results <- err
				return
			}
			if !bytes.Equal(pair.Value, pair.Revision) {
				results <- errors.New("torn model snapshot")
				return
			}
		}
		results <- nil
	}()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	final, err := repo.GetLLMProviderSnapshot(ctx)
	if err != nil || string(final.Value) != strconv.Itoa(writers) || !bytes.Equal(final.Value, final.Revision) {
		t.Fatal("concurrent update lost or snapshot torn")
	}
	rollbackErr := errors.New("abort callback")
	if err := repo.UpdateLLMProviders(ctx, func(LLMProviderSnapshot) (LLMProviderSnapshot, error) { return LLMProviderSnapshot{}, rollbackErr }); !errors.Is(err, rollbackErr) {
		t.Fatal("callback error not propagated")
	}
	after, err := repo.GetLLMProviderSnapshot(ctx)
	if err != nil || !bytes.Equal(after.Value, final.Value) || !bytes.Equal(after.Revision, final.Revision) {
		t.Fatal("callback failure modified settings")
	}
}

func TestModelSettingsSQLiteConcurrency(t *testing.T) {
	assertModelSettingsConcurrency(t, modelRepositoryDB(t))
}

func TestModelSettingsMySQLTransaction(t *testing.T) {
	dsn := os.Getenv("QUANT4DAD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUANT4DAD_TEST_MYSQL_DSN is not set")
	}
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendMySQL, MySQL: config.MySQLConfig{DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.Setting{}); err != nil {
		t.Fatal(err)
	}
	// Use only the dedicated test database, restoring any prior setting rows.
	var original []domain.Setting
	keys := []string{domain.SettingKeyLLMProviders, domain.SettingKeyAgentModelRevision}
	if err := db.Where("`key` IN ?", keys).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("`key` IN ?", keys).Delete(&domain.Setting{}).Error; err != nil {
				return err
			}
			if len(original) > 0 {
				return tx.Create(&original).Error
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})
	assertModelSettingsConcurrency(t, db)
}
