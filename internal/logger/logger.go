// Package logger provides JSON logs on stderr, leaving stdout for CLI and MCP.
package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"sync/atomic"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Level    string
	BaseName string
	Stderr   bool
}

var active atomic.Pointer[zap.Logger]

func init() { active.Store(zap.NewNop()) }
func Init(cfg Config) error {
	level := zapcore.InfoLevel
	if cfg.Level != "" {
		if err := level.Set(cfg.Level); err != nil {
			return err
		}
	}
	c := zap.NewProductionConfig()
	c.Level = zap.NewAtomicLevelAt(level)
	c.OutputPaths, c.ErrorOutputPaths = []string{"stderr"}, []string{"stderr"}
	c.DisableStacktrace = true
	l, err := c.Build()
	if err != nil {
		return err
	}
	old := active.Swap(l)
	_ = old.Sync()
	return nil
}
func Shutdown()      { _ = L().Sync() }
func L() *zap.Logger { return active.Load() }
func LogStruct(label string, v any) {
	value, _ := sonic.MarshalString(v)
	L().Info(label, zap.String("data", value))
}

const TraceHeader = "X-Request-ID"

type traceKey struct{}

var validTrace = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func ContextWithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)
	return id
}
func requestContext(w http.ResponseWriter, r *http.Request) *http.Request {
	id := r.Header.Get(TraceHeader)
	if !validTrace.MatchString(id) {
		var raw [16]byte
		_, _ = rand.Read(raw[:])
		id = hex.EncodeToString(raw[:])
	}
	w.Header().Set(TraceHeader, id)
	return r.WithContext(ContextWithTraceID(r.Context(), id))
}
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, requestContext(w, r)) })
}
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) { c.Request = requestContext(c.Writer, c.Request); c.Next() }
}
func withTrace(ctx context.Context) *zap.Logger {
	if id := TraceIDFromContext(ctx); id != "" {
		return L().With(zap.String("trace_id", id))
	}
	return L()
}
func Info(ctx context.Context, msg string, fields ...zap.Field) { withTrace(ctx).Info(msg, fields...) }
func Warn(ctx context.Context, msg string, fields ...zap.Field) { withTrace(ctx).Warn(msg, fields...) }
func Error(ctx context.Context, msg string, fields ...zap.Field) {
	withTrace(ctx).Error(msg, fields...)
}
