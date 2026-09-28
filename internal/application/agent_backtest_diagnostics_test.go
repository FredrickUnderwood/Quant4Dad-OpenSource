package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestAgentBacktestFailureDiagnostics(t *testing.T) {
	const secret = "private-backtest-diagnostic-marker"
	if os.Getenv("Q4D_BACKTEST_LOG_TEST_CHILD") == "1" {
		if err := logger.Init(logger.Config{Level: "info"}); err != nil {
			t.Fatal("logger initialization failed")
		}
		defer logger.Shutdown()
		db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "diagnostics.db")}})
		if err != nil {
			t.Fatal("database open failed")
		}
		t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
		if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
			t.Fatal("migration failed")
		}
		repo := repository.NewBacktestRepository(db)
		svc := service.NewBacktestService(repo, repository.NewTradeRepository(db), repository.NewEquityRepository(db))
		app := NewBacktestApp(svc, service.NewStrategyService(nil), nil, repository.NewGormBarRepository(db), 1)
		for _, reject := range []bool{false, true} {
			if reject {
				if db.Exec("CREATE TRIGGER reject_failure BEFORE UPDATE ON backtest_job WHEN NEW.error_msg <> '' BEGIN SELECT RAISE(ABORT, '"+secret+"'); END").Error != nil {
					t.Fatal("could not inject failure persistence error")
				}
			}
			job := &domain.BacktestJob{Status: domain.BacktestStatusPending, AgentRun: true, AgentSnapshot: secret}
			if repo.CreateJob(context.Background(), job) != nil {
				t.Fatal("create job failed")
			}
			stop := app.StartAgentWorker()
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				if db.First(job, job.ID).Error != nil {
					stop()
					t.Fatal("job read failed")
				}
				if job.Status != domain.BacktestStatusPending {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			stop() // waits for the claimed job and its failure persistence attempt
			if db.First(job, job.ID).Error != nil || (!reject && job.Status != domain.BacktestStatusFailed) || (reject && job.Status != domain.BacktestStatusRunning) {
				t.Fatal("unexpected persisted outcome")
			}
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentBacktestFailureDiagnostics$")
	cmd.Env = append(os.Environ(), "Q4D_BACKTEST_LOG_TEST_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnostics subprocess failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), secret) {
		t.Fatal("private snapshot or database error escaped to logs")
	}
	execution, persistence, persisted := false, false, false
	for _, line := range strings.Split(string(output), "\n") {
		var row map[string]any
		if sonic.UnmarshalString(line, &row) != nil {
			continue
		}
		switch row["msg"] {
		case "agent backtest execution failed":
			execution = row["job_id"] != nil && row["stage"] == "snapshot" && row["error_code"] == "invalid_input"
		case "agent backtest failure persistence failed":
			persistence = row["job_id"] != nil && row["stage"] == "persist_failure" && row["error_code"] == "operation_failed"
		case "agent backtest failure persisted":
			persisted = row["job_id"] != nil
		}
	}
	if !execution || !persistence || !persisted {
		t.Fatalf("missing safe diagnostics: execution=%v persistence=%v persisted=%v\n%s", execution, persistence, persisted, output)
	}
}
