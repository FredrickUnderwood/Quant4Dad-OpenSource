//go:build cgo

package logger

import (
	"fmt"
	"syscall"
	"testing"

	"github.com/mattn/go-sqlite3"
	"go.uber.org/zap/zapcore"
)

func TestSQLiteErrorFieldsKeepCodesWithoutPrivateData(t *testing.T) {
	driverErr := sqlite3.Error{Code: sqlite3.ErrIoErr, ExtendedCode: sqlite3.ErrIoErrWrite, SystemErrno: syscall.ENOSPC}
	for _, err := range []error{driverErr, fmt.Errorf("private SQL and credentials: %w", driverErr)} {
		encoded := zapcore.NewMapObjectEncoder()
		for _, field := range DatabaseErrorFields(err) {
			field.AddTo(encoded)
		}
		if len(encoded.Fields) != 5 || encoded.Fields["sqlite_code"] != int64(sqlite3.ErrIoErr) ||
			encoded.Fields["sqlite_extended_code"] != int64(sqlite3.ErrIoErrWrite) ||
			encoded.Fields["sqlite_system_errno"] != int64(syscall.ENOSPC) ||
			encoded.Fields["sqlite_reason"] != "unclassified" ||
			encoded.Fields["error_type"] != fmt.Sprintf("%T", err) {
			t.Fatalf("unexpected SQLite diagnostics: %v", encoded.Fields)
		}
	}
}

func TestSQLiteReasonAllowlist(t *testing.T) {
	for message, want := range map[string]string{
		"database is locked":                                     "database_locked",
		"database table is locked":                               "table_locked",
		"database schema is locked":                              "schema_locked",
		"cannot commit transaction - SQL statements in progress": "statements_in_progress",
		"database is locked: private credentials":                "unclassified",
		"UNIQUE constraint failed: private input":                "unclassified",
	} {
		if got := sqliteErrorReason(message); got != want {
			t.Errorf("SQLite reason = %q, want %q", got, want)
		}
	}
}
