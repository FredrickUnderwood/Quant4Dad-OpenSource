package repository

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

var migrationContractTables = []string{
	"instrument",
	"strategy",
	"cost",
	"backtest_job",
	"backtest_result",
	"trade",
	"equity_point",
	"data_sync_task",
	"data_sync_failure",
	"data_coverage_summary",
	"pipeline",
	"pipeline_node",
	"pipeline_edge",
	"pipeline_event",
	"pipeline_event_trace",
	"pipeline_ai_result",
	"setting",
	"agent_session_binding",
	"agent_request_binding",
	"agent_tool_audit",
	"agent_approval_receipt",
	"agent_tool_effect",
	"news",
	"news_subscription",
	"bar",
}

func assertMigrationContract(t *testing.T, db *gorm.DB, backend string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if err := Migrate(db, backend); err != nil {
			t.Fatalf("migration pass %d: %v", i+1, err)
		}
	}
	for _, table := range migrationContractTables {
		if !db.Migrator().HasTable(table) {
			t.Errorf("missing migrated table %q", table)
		}
	}
	assertAgentSessionConstraints(t, db)
	assertAgentRunConstraints(t, db)
	assertAgentToolAuditConstraints(t, db)
}

func assertAgentToolAuditConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	one := domain.AgentToolAudit{ID: "audit-1", ActorID: "Owner", SessionID: "session", RunID: "run", ToolCallID: "call", ToolName: "query_kline", IdempotencyKey: "key", ArgsHash: "hash", EnvelopeDigest: "envelope", Risk: "R0", Status: "executing"}
	if err := tx.Create(&one).Error; err != nil {
		t.Fatal(err)
	}
	two := one
	two.ID = "audit-2"
	if err := tx.Create(&two).Error; err == nil {
		t.Fatal("duplicate logical Tool call accepted")
	}
	two.ToolCallID = "other-call"
	if err := tx.Create(&two).Error; err == nil {
		t.Fatal("duplicate actor/tool/idempotency key accepted")
	}
	two.ActorID = "owner"
	if err := tx.Create(&two).Error; err != nil {
		t.Fatal("actor comparison must be binary", err)
	}
}

func assertAgentRunConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	rows := []domain.AgentRequestBinding{{ID: "run-constraint-1", SessionID: "session-1", ActorID: "Owner", ClientRequestKey: "key"}, {ID: "run-constraint-2", SessionID: "session-2", ActorID: "owner", ClientRequestKey: "key"}, {ID: "run-constraint-3", SessionID: "session-1", ActorID: "Owner", ClientRequestKey: "different"}}
	for _, row := range rows {
		if err := tx.Create(&row).Error; err != nil {
			t.Fatal("NULL message IDs or different Session keys conflicted", err)
		}
	}
	duplicate := rows[0]
	duplicate.ID = "run-constraint-4"
	if err := tx.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate Session/client binding accepted")
	}
	var count int64
	if err := tx.Model(&domain.AgentRequestBinding{}).Where("actor_id = ?", "Owner").Count(&count).Error; err != nil || count != 2 {
		t.Fatal("actor comparison is not binary", count, err)
	}
}

func TestMigrateSQLiteContract(t *testing.T) {
	db, err := Open(config.StorageConfig{
		Backend: config.StorageBackendSQLite,
		SQLite:  config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "migration.db")},
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	assertMigrationContract(t, db, config.StorageBackendSQLite)
}

func TestMigrateMySQLContract(t *testing.T) {
	dsn := os.Getenv("QUANT4DAD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUANT4DAD_TEST_MYSQL_DSN is not set")
	}
	db, err := Open(config.StorageConfig{
		Backend: config.StorageBackendMySQL,
		MySQL:   config.MySQLConfig{DSN: dsn},
	})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	assertMigrationContract(t, db, config.StorageBackendMySQL)
}

// Run the same identity/NULL uniqueness checks on both migrated SQL backends.
func assertAgentSessionConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	rows := []domain.AgentSessionBinding{{ID: "session-constraint-1", ActorID: "ConstraintOwner", ProvisionRequestKey: "same-key"}, {ID: "session-constraint-2", ActorID: "constraintowner", ProvisionRequestKey: "same-key"}, {ID: "session-constraint-3", ActorID: "ConstraintOwner", ProvisionRequestKey: "different-key"}}
	for _, row := range rows {
		if err := tx.Create(&row).Error; err != nil {
			t.Fatal("NULL DSH IDs or binary actor identities conflicted", err)
		}
	}
	duplicate := rows[0]
	duplicate.ID = "session-constraint-4"
	if err := tx.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate actor/request key accepted")
	}
	dsh := "shared-dsh-id"
	if err := tx.Model(&domain.AgentSessionBinding{}).Where("id = ?", rows[0].ID).Update("dsh_session_id", dsh).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&domain.AgentSessionBinding{}).Where("id = ?", rows[1].ID).Update("dsh_session_id", dsh).Error; err == nil {
		t.Fatal("duplicate DSH binding accepted")
	}
}
