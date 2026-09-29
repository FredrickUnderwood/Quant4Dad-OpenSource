//go:build cgo

package logger

import (
	"errors"

	"github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

func appendSQLiteErrorFields(fields []zap.Field, err error) []zap.Field {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		fields = append(fields, zap.Int("sqlite_code", int(sqliteErr.Code)),
			zap.Int("sqlite_extended_code", int(sqliteErr.ExtendedCode)),
			zap.Int("sqlite_system_errno", int(sqliteErr.SystemErrno)),
			zap.String("sqlite_reason", sqliteErrorReason(sqliteErr.Error())))
	}
	return fields
}

// Match complete, known driver messages only. Never log arbitrary error text:
// constraint and connection errors can contain user values or credentials.
func sqliteErrorReason(message string) string {
	switch message {
	case "database is locked":
		return "database_locked"
	case "database table is locked":
		return "table_locked"
	case "database schema is locked":
		return "schema_locked"
	case "cannot commit transaction - SQL statements in progress":
		return "statements_in_progress"
	default:
		return "unclassified"
	}
}
