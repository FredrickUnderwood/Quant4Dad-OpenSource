package logger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// GORM routes database diagnostics through the process SDK logger. SQL text,
// arguments and driver error messages may contain credentials or stored news / AI
// content, so only timing, row counts, error types and database codes are emitted.
func GORM() gormlogger.Interface {
	return gormLog{level: gormlogger.Warn, slowThreshold: 200 * time.Millisecond}
}

type agentToolLogKey struct{}

// AgentToolContext attaches only bounded correlation identifiers to database
// diagnostics; tool arguments and result bodies must never enter these fields.
func AgentToolContext(ctx context.Context, session, run, call, tool string) context.Context {
	return context.WithValue(ctx, agentToolLogKey{}, []zap.Field{
		zap.String("session_id", session), zap.String("run_id", run),
		zap.String("tool_call_id", call), zap.String("tool", tool),
	})
}

type gormLog struct {
	level         gormlogger.LogLevel
	slowThreshold time.Duration
}

func (l gormLog) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.level = level
	return l
}

// GORM's formatted messages can include both SQL values and driver errors.
// Preserve their severity without formatting or serializing the supplied data.
func (l gormLog) Info(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Info {
		Info(ctx, "database diagnostic")
	}
}

func (l gormLog) Warn(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Warn {
		Warn(ctx, "database warning")
	}
}

func (l gormLog) Error(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Error {
		Error(ctx, "database error")
	}
}

func (l gormLog) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	failed := err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && l.level >= gormlogger.Error
	slow := elapsed > l.slowThreshold && l.level >= gormlogger.Warn
	if !failed && !slow && l.level < gormlogger.Info {
		return
	}
	_, rows := fc()
	fields := []zap.Field{zap.Duration("elapsed", elapsed), zap.Int64("rows", rows)}
	if correlation, ok := ctx.Value(agentToolLogKey{}).([]zap.Field); ok {
		fields = append(fields, correlation...)
	}
	switch {
	case failed:
		fields = append(fields, DatabaseErrorFields(err)...)
		Error(ctx, "database query failed", fields...)
	case slow:
		Warn(ctx, "database query slow", fields...)
	default:
		Info(ctx, "database query completed", fields...)
	}
}

// DatabaseErrorFields preserves actionable driver codes without logging SQL,
// arguments or error messages, which may contain private application data.
func DatabaseErrorFields(err error) []zap.Field {
	fields := []zap.Field{zap.String("error_type", fmt.Sprintf("%T", err))}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		fields = append(fields, zap.Uint16("mysql_errno", mysqlErr.Number), zap.String("sql_state", string(mysqlErr.SQLState[:])))
	}
	return appendSQLiteErrorFields(fields, err)
}

// ParamsFilter is GORM's parameterized-logging hook. Even the unlogged SQL passed
// to Trace keeps placeholders; query arguments are never interpolated for logs.
func (l gormLog) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}
