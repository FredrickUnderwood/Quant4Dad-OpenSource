//go:build !cgo

package logger

import "go.uber.org/zap"

// The Web binary has no SQLite driver and is built without CGO.
func appendSQLiteErrorFields(fields []zap.Field, _ error) []zap.Field { return fields }
