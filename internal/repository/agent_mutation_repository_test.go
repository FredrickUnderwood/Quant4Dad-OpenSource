package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

func TestAgentBacktestEnqueueSQLite(t *testing.T) {
	assertAgentBacktestEnqueue(t, modelRepositoryDB(t))
}

func TestAgentBacktestEnqueueMySQL(t *testing.T) {
	dsn := os.Getenv("QUANT4DAD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUANT4DAD_TEST_MYSQL_DSN is not set")
	}
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendMySQL, MySQL: config.MySQLConfig{DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	assertAgentBacktestEnqueue(t, db)
}

// Exercise the real enqueue transaction on both dialects. SQLite accepts the
// unquoted MySQL keyword "key", so only the MySQL case catches that regression.
func assertAgentBacktestEnqueue(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&domain.Setting{}, &domain.Strategy{}, &domain.BacktestJob{},
		&domain.AgentRequestBinding{}, &domain.AgentToolAudit{}, &domain.AgentToolEffect{}); err != nil {
		t.Fatal(err)
	}
	// All fixture rows and the queue guard are rolled back in the test database.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	strategy := domain.Strategy{Name: "agent-backtest-enqueue", Version: 1, Body: []byte(`{}`)}
	run := domain.AgentRequestBinding{ID: "enqueue-run", SessionID: "enqueue-session", ActorID: "enqueue-actor",
		ClientRequestKey: "enqueue-request", EnvelopeDigest: "enqueue-envelope", DeadlineMS: time.Now().Add(time.Minute).UnixMilli()}
	for _, row := range []any{&strategy, &run} {
		if err := tx.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := sonic.MarshalString(domain.AgentBacktestSnapshot{Strategy: strategy})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewAgentMutationRepository(tx)
	ctx := context.Background()
	var lastID int64
	for i := range 2 {
		// The second call must reuse the same database queue mutex successfully.
		audit := domain.AgentToolAudit{ID: fmt.Sprintf("enqueue-audit-%d", i), ActorID: run.ActorID, SessionID: run.SessionID,
			RunID: run.ID, ToolCallID: fmt.Sprintf("enqueue-call-%d", i), ToolName: "run_backtest",
			IdempotencyKey: fmt.Sprintf("enqueue-key-%d", i), ArgsHash: "enqueue-hash", EnvelopeDigest: run.EnvelopeDigest,
			Risk: "R1", Status: "executing", Started: true}
		if err := tx.Create(&audit).Error; err != nil {
			t.Fatal(err)
		}
		job := &domain.BacktestJob{StrategyID: strategy.ID, InitialCapital: 10000000,
			StartDate: time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
			AgentRun: true, AgentSnapshot: snapshot}
		mutation := domain.AgentMutation{Kind: "run_backtest", Backtest: job}
		result, err := repo.Execute(ctx, audit, domain.AgentApprovalReceipt{}, mutation)
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		if result.JobID <= lastID || result.Status != "pending" {
			t.Fatalf("unexpected enqueue result: %+v", result)
		}
		lastID = result.JobID
		var stored domain.BacktestJob
		if err := tx.First(&stored, result.JobID).Error; err != nil || !stored.AgentRun || stored.Status != domain.BacktestStatusPending || stored.AgentSnapshot != snapshot {
			t.Fatalf("queued job lost its state or snapshot: %+v, %v", stored, err)
		}
		replayed, err := repo.Execute(ctx, audit, domain.AgentApprovalReceipt{}, mutation)
		if err != nil || replayed != result {
			t.Fatalf("idempotent replay: %+v, %v", replayed, err)
		}
	}
	var jobs, effects int64
	if err := tx.Model(&domain.BacktestJob{}).Where("strategy_id = ?", strategy.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&domain.AgentToolEffect{}).Where("audit_id IN ?", []string{"enqueue-audit-0", "enqueue-audit-1"}).Count(&effects).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || effects != 2 {
		t.Fatalf("enqueue/replay created %d jobs and %d effects, want 2 each", jobs, effects)
	}
}
