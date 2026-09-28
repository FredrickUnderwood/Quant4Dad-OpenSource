package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestImportCLIProducesReviewableCountsAndPersistsOnlyAfterDryRun(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: dbPath}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&domain.Instrument{}, &domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  backend: sqlite\n  sqlite:\n    path: "+dbPath+"\nlog:\n  level: error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-config", configPath, "-instruments", "../../examples/import/instruments.csv", "-bars", "../../examples/import/bars.csv"}
	for _, dry := range []bool{true, false} {
		args := append([]string{}, base...)
		if dry {
			args = append(args, "-dry-run")
		}
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatalf("CLI: %v %s", err, stderr.String())
		}
		var result service.CSVImportResult
		if err := sonic.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("stdout is not JSON: %q %v", stdout.String(), err)
		}
		if result.DryRun != dry || result.Instruments.Inserted != 2 || result.Bars.Inserted != 160 {
			t.Fatalf("counts: %+v", result)
		}
		var count int64
		if err := db.Model(&domain.Bar{}).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		want := int64(160)
		if dry {
			want = 0
		}
		if count != want {
			t.Fatalf("persisted %d want %d", count, want)
		}
	}
}

func TestImportCLIDryRunNeverCreatesMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("storage:\n  backend: sqlite\n  sqlite:\n    path: "+dbPath+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-config", cfg, "-instruments", "../../examples/import/instruments.csv", "-dry-run"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "database does not exist") {
		t.Fatalf("expected missing database, got %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("dry run created a database: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatal("failure emitted a success result")
	}
}
