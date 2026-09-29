package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

const csvDiagnosticSecret = "private-csv-value-and-database-credentials"

func TestCSVImportTransactionDiagnosticsAndRollback(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCSVImportDiagnosticsProcess$")
	cmd.Env = append(os.Environ(), "Q4D_CSV_DIAGNOSTIC_TEST_DB="+filepath.Join(t.TempDir(), "import.db"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("CSV diagnostic process failed: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("database logs contaminated CLI stdout: %s", stdout.String())
	}
	for _, forbidden := range []string{csvDiagnosticSecret, "INSERT", "SELECT", "COMMIT", "import.db"} {
		if bytes.Contains(stderr.Bytes(), []byte(forbidden)) {
			t.Fatalf("CSV diagnostics exposed SQL, input or connection details: %s", stderr.String())
		}
	}
	seen := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("invalid JSON diagnostic: %s", line)
		}
		if entry["msg"] != "CSV import transaction failed" {
			continue
		}
		phase, _ := entry["phase"].(string)
		seen[phase] = true
		if entry["backend"] != "sqlite" || entry["dry_run"] != false || entry["error_type"] == nil ||
			entry["elapsed"] == nil || entry["trace_id"] != "csv-import-diagnostic" {
			t.Fatalf("missing actionable transaction context: %v", entry)
		}
		if phase == "commit" && (entry["sqlite_code"] != float64(sqlite3.ErrBusy) ||
			entry["sqlite_extended_code"] != float64(sqlite3.ErrBusy) || entry["sqlite_system_errno"] != float64(0) ||
			entry["sqlite_reason"] != "database_locked") {
			t.Fatalf("missing SQLite commit error codes: %v", entry)
		}
	}
	for _, phase := range []string{"begin", "apply", "commit"} {
		if !seen[phase] {
			t.Errorf("missing %s transaction diagnostic: %s", phase, stderr.String())
		}
	}
}

func TestCSVImportDiagnosticsProcess(t *testing.T) {
	path := os.Getenv("Q4D_CSV_DIAGNOSTIC_TEST_DB")
	if path == "" {
		return
	}
	if err := logger.Init(logger.Config{Level: "info"}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(config.StorageConfig{Backend: config.StorageBackendSQLite,
		SQLite: config.SQLiteConfig{Path: path + "?_busy_timeout=100"}})
	if err != nil {
		t.Fatal(err)
	}
	writer, _ := db.DB()
	defer writer.Close()
	if err := db.AutoMigrate(&domain.Instrument{}); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	held, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	var count int
	if err := held.QueryRow("SELECT COUNT(*) FROM instrument").Scan(&count); err != nil {
		t.Fatal(err)
	}
	ctx := logger.ContextWithTraceID(context.Background(), "csv-import-diagnostic")
	repo := NewCSVImportRepository(db)
	insert := func(tx *CSVImportTransaction) error {
		return tx.CreateInstruments(ctx, []*domain.Instrument{{Code: "sh.999999", Name: csvDiagnosticSecret}})
	}
	// A held read transaction permits INSERT but prevents COMMIT in SQLite's
	// default rollback-journal mode. This exercises the real driver failure.
	err = repo.Transaction(ctx, false, insert)
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
		t.Fatalf("expected commit SQLITE_BUSY, got %T: %v", err, err)
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRow("SELECT COUNT(*) FROM instrument").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed commit was not rolled back: count=%d error=%v", count, err)
	}
	if err := repo.Transaction(ctx, false, insert); err != nil {
		t.Fatalf("retry after reader releases: %v", err)
	}
	if err := writer.QueryRow("SELECT COUNT(*) FROM instrument").Scan(&count); err != nil || count != 1 {
		t.Fatalf("successful commit missing: count=%d error=%v", count, err)
	}
	privateErr := errors.New(csvDiagnosticSecret)
	if err := repo.Transaction(ctx, false, func(*CSVImportTransaction) error { return privateErr }); err != privateErr {
		t.Fatalf("transaction changed callback error identity: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transaction(ctx, false, func(*CSVImportTransaction) error {
		t.Fatal("callback entered after BeginTx failed")
		return nil
	}); err == nil {
		t.Fatal("expected BeginTx to fail on a closed database")
	}
	logger.Shutdown()
	os.Exit(0)
}
