package logger

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestJSONLogsUseStderr(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoggerProcess$")
	cmd.Env = append(os.Environ(), "QUANT4DAD_LOG_TEST_CHILD=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("logger process failed: %v: %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("logs contaminated MCP stdout: %s", stdout.String())
	}
	for name, output := range map[string][]byte{"stderr": stderr.Bytes()} {
		lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
		if len(lines) != 3 {
			t.Fatalf("%s: expected three log entries, got %s", name, output)
		}
		if bytes.Contains(output, []byte("private-diagnostic")) {
			t.Fatalf("%s: database diagnostic leaked private data", name)
		}
		for i, line := range lines {
			var fields map[string]any
			if err := sonic.Unmarshal(line, &fields); err != nil {
				t.Fatalf("%s: invalid JSON log: %v", name, err)
			}
			if i == 2 {
				for key, want := range map[string]string{"session_id": "session-fixture", "run_id": "run-fixture", "tool_call_id": "call-fixture", "tool": "run_backtest", "sql_state": "42000"} {
					if fields[key] != want {
						t.Errorf("%s: missing database correlation %s: %v", name, key, fields)
					}
				}
				if fields["mysql_errno"] != float64(1064) {
					t.Errorf("%s: missing MySQL error number: %v", name, fields)
				}
			}
			if i == 1 && fields["trace_id"] != "gateway-trace" {
				t.Errorf("%s: missing context trace: %v", name, fields)
			}
		}
	}
}

func TestLoggerProcess(t *testing.T) {
	if os.Getenv("QUANT4DAD_LOG_TEST_CHILD") != "1" {
		return
	}
	if err := Init(Config{Level: "info", BaseName: "local-log-name", Stderr: false}); err != nil {
		t.Fatal(err)
	}
	L().Info("facade log")
	Info(ContextWithTraceID(context.Background(), "gateway-trace"), "request log")
	ctx := AgentToolContext(context.Background(), "session-fixture", "run-fixture", "call-fixture", "run_backtest")
	GORM().Trace(ctx, time.Now(), func() (string, int64) { return "private-diagnostic SQL", 0 },
		&mysql.MySQLError{Number: 1064, SQLState: [5]byte{'4', '2', '0', '0', '0'}, Message: "private-diagnostic driver message"})
	Shutdown()
	os.Exit(0)
}

func TestGORMExpectedMissingRowIsSilent(t *testing.T) {
	for _, err := range []error{gorm.ErrRecordNotFound, fmt.Errorf("wrapped: %w", gorm.ErrRecordNotFound)} {
		GORM().Trace(context.Background(), time.Now(), func() (string, int64) {
			t.Fatal("normal empty queue must not format or emit a database error")
			return "private SQL", 0
		}, err)
	}
}
