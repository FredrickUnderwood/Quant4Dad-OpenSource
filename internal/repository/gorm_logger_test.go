package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/quant4dad/internal/logger"
)

const databaseLogSecret = "private-database-key-and-news-body"

func TestGORMLogsWithoutSQLValues(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGORMLoggerProcess$")
	cmd.Env = append(os.Environ(), "QUANT4DAD_GORM_TEST_CHILD=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("GORM logger process failed: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("GORM logs contaminated MCP stdout: %s", stdout.String())
	}
	for name, output := range map[string][]byte{"stderr": stderr.Bytes()} {
		if bytes.Contains(output, []byte(databaseLogSecret)) || bytes.Contains(output, []byte("SELECT")) {
			t.Fatalf("%s exposed SQL text, parameters or driver error contents: %s", name, output)
		}
		seen := map[string]bool{}
		for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
			var entry map[string]any
			if err := sonic.Unmarshal(line, &entry); err != nil {
				t.Fatalf("%s did not contain JSON logs: %v", name, err)
			}
			if entry["trace_id"] != "database-trace" {
				t.Errorf("%s: missing request trace: %v", name, entry)
			}
			msg, _ := entry["msg"].(string)
			seen[msg] = true
			if msg == "database query failed" && entry["error_type"] == nil {
				t.Errorf("%s: missing safe error type: %v", name, entry)
			}
		}
		for _, msg := range []string{"database query failed", "database query slow", "database error", "database warning", "database diagnostic"} {
			if !seen[msg] {
				t.Errorf("%s: missing %s", name, msg)
			}
		}
	}
}

func TestGORMLoggerProcess(t *testing.T) {
	if os.Getenv("QUANT4DAD_GORM_TEST_CHILD") != "1" {
		return
	}
	if err := logger.Init(logger.Config{Level: "info"}); err != nil {
		t.Fatal(err)
	}
	ctx := logger.ContextWithTraceID(context.Background(), "database-trace")
	cfg := defaultGormConfig()
	filter, ok := cfg.Logger.(gorm.ParamsFilter)
	if !ok {
		t.Fatal("GORM logger does not disable parameter interpolation")
	}
	if sql, params := filter.ParamsFilter(ctx, "SELECT ?", databaseLogSecret); sql != "SELECT ?" || len(params) != 0 {
		t.Fatal("GORM logger retained SQL values")
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec("SELECT '" + databaseLogSecret + "' FROM missing_table").Error; err == nil {
		t.Fatal("expected a real database error")
	}
	fc := func() (string, int64) { return "SELECT '" + databaseLogSecret + "'", 7 }
	cfg.Logger.Trace(ctx, time.Now().Add(-time.Second), fc, nil)
	cfg.Logger.Trace(ctx, time.Now(), fc, errors.New(databaseLogSecret))
	cfg.Logger.Error(ctx, "database connection %s failed: %v", databaseLogSecret, errors.New(databaseLogSecret))
	cfg.Logger.Warn(ctx, databaseLogSecret)
	cfg.Logger.LogMode(gormlogger.Info).Info(ctx, "data: %s", databaseLogSecret)
	silent := cfg.Logger.LogMode(gormlogger.Silent)
	silent.Trace(ctx, time.Now(), func() (string, int64) {
		t.Fatal("silent logger formatted a query")
		return "", 0
	}, errors.New(databaseLogSecret))
	logger.Shutdown()
	os.Exit(0)
}
