package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

func TestAgentEventPayloadTextSQLite(t *testing.T) {
	assertAgentEventPayloadText(t, modelRepositoryDB(t))
}

func TestAgentEventPayloadTextMySQL(t *testing.T) {
	dsn := os.Getenv("QUANT4DAD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUANT4DAD_TEST_MYSQL_DSN is not set")
	}
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendMySQL, MySQL: config.MySQLConfig{DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	assertAgentEventPayloadText(t, db)
}

func assertAgentEventPayloadText(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&domain.Event{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	repo := NewAgentCatalogQueryRepository(tx)
	payloads := []string{`{"text":"fixture 中文"}`, `{"text":"` + strings.Repeat("a", 9000) + `"}`}
	for i, payload := range payloads {
		uid := []string{"catalog-text-short", "catalog-text-long"}[i]
		event := domain.Event{EventUID: uid, PipelineID: 1, Status: domain.EventStatusPassed, ReceivedAt: time.Now().UTC(), FinalPayload: []byte(payload)}
		if err := tx.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		got, err := repo.Get(context.Background(), "event", event.ID)
		if err != nil {
			t.Fatal(err)
		}
		// Check the JSON seen by a model, not the database driver's Go value.
		body, err := sonic.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Text      string `json:"final_payload_text"`
			Truncated int    `json:"payload_truncated"`
		}
		if err := sonic.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		want := payload
		if i == 1 {
			want = payload[:8192]
		}
		if wire.Text != want || wire.Truncated != i {
			t.Fatalf("event payload must be bounded plaintext, not base64: length=%d truncated=%d", len(wire.Text), wire.Truncated)
		}
	}
	if _, err := repo.Get(context.Background(), "event", 9007199254740991); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing event: %v", err)
	}
}
