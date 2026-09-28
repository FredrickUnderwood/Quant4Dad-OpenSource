// Package observability exports process and bounded HTTP metrics on /metrics.
package observability

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var registry = prometheus.NewRegistry()
var requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "Completed HTTP requests."}, []string{"method", "route", "status"})
var duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request duration.", Buckets: prometheus.DefBuckets}, []string{"method", "route"})

func init() {
	registry.MustRegister(requests, duration, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
}

// The existing application listener owns the endpoint and its shutdown.
func InitMetrics() error    { return nil }
func ShutdownMetrics()      {}
func Handler() http.Handler { return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}) }
func ObserveHTTP(route, method string, status int, elapsed time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		method = "OTHER"
	}
	requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	duration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func Middleware(next http.Handler, route func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		writer := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r)
		name := r.Pattern
		if _, path, ok := strings.Cut(name, " "); ok {
			name = path
		}
		if route != nil {
			name = route(r)
		}
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		ObserveHTTP(name, r.Method, status, time.Since(start))
	})
}
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		ObserveHTTP(c.FullPath(), c.Request.Method, c.Writer.Status(), time.Since(start))
	}
}
