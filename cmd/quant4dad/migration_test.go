package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func TestReleaseMigrationContract(t *testing.T) {
	t.Setenv("QUANT4DAD_CONFIG", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("storage:\n  sqlite:\n    path: "+filepath.Join(dir, "data.db")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		mode string
		code int
	}{{"check", 10}, {"compatible", 1}, {"apply", 0}, {"check", 0}, {"apply", 0}, {"compatible", 0}, {"bogus", 1}} {
		if code := runMigration(file, check.mode); code != check.code {
			t.Fatalf("%s: got %d want %d", check.mode, code, check.code)
		}
	}
	cfg, _ := config.Load(file)
	db, err := repository.Open(cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.Migrator().DropTable("news_subscription"); err != nil {
		t.Fatal(err)
	}
	if runMigration(file, "check") != 10 {
		t.Fatal("schema drift went undetected")
	}
	if db.Migrator().HasTable("news_subscription") {
		t.Fatal("check unexpectedly migrated schema")
	}
}

func TestSessionTitleMigrationPreservesExistingSessions(t *testing.T) {
	t.Setenv("QUANT4DAD_CONFIG", "")
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("storage:\n  sqlite:\n    path: "+filepath.Join(filepath.Dir(file), "title.db")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runMigration(file, "apply"); code != 0 {
		t.Fatal(code)
	}
	cfg, _ := config.Load(file)
	db, err := repository.Open(cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	row := domain.AgentSessionBinding{ID: "existing-session", ActorID: "owner", ProvisionRequestKey: "key", Title: "已有会话标题", Status: domain.AgentSessionActive}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	columns := []string{"title_generation", "title_settled_generation", "title_attempt", "title_lease_until_ms"}
	for _, column := range columns {
		if err := db.Migrator().DropColumn(&domain.AgentSessionBinding{}, column); err != nil {
			t.Fatal(err)
		}
	}
	// GORM's SQLite DropColumn rebuilds the table and removes secondary
	// indexes. Restore the four pre-existing indexes to model a real old DB.
	for _, name := range []string{"idx_agent_session_actor_id", "uk_agent_session_provision", "idx_agent_session_reconcile", "uk_agent_session_dsh"} {
		if err := db.Migrator().CreateIndex(&domain.AgentSessionBinding{}, name); err != nil {
			t.Fatal(err)
		}
	}
	if check, compatible := runMigration(file, "check"), runMigration(file, "compatible"); check != 10 || compatible != 0 {
		t.Fatal("additive title migration was not admitted", check, compatible)
	}
	for _, column := range columns {
		if db.Migrator().HasColumn(&domain.AgentSessionBinding{}, column) {
			t.Fatal("compatibility check performed DDL")
		}
	}
	if runMigration(file, "apply") != 0 || runMigration(file, "check") != 0 || runMigration(file, "apply") != 0 {
		t.Fatal("migration was not idempotent")
	}
	var saved domain.AgentSessionBinding
	if err := db.First(&saved, "id = ?", row.ID).Error; err != nil || saved.Title != row.Title || saved.TitleGeneration != 0 || saved.TitleSettledGeneration != 0 || saved.TitleAttempt != "" || saved.TitleLeaseUntilMS != 0 {
		t.Fatal(saved, err)
	}
	if err := db.Migrator().DropColumn(&domain.AgentSessionBinding{}, "title"); err != nil {
		t.Fatal(err)
	}
	if runMigration(file, "compatible") != 1 {
		t.Fatal("unrelated schema drift bypassed compatibility check")
	}
}

func TestCompatibilityIsReadOnlyAndRequiresSchema(t *testing.T) {
	t.Setenv("QUANT4DAD_CONFIG", "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "absent.db")
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("storage:\n  sqlite:\n    path: "+dbPath+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if runMigration(file, "compatible") != 1 {
		t.Fatal("absent schema accepted")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("check created a database")
	}
}
