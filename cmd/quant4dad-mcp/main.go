// Command quant4dad-mcp is quant4dad's MCP (Model Context Protocol) server. It
// exposes the complete Agent tool catalog through the API, with an optional
// legacy mode for bounded read-only market, event and pipeline queries.
//
// Two transports:
//
//   - stdio (default): started as a child process by the MCP client, with the
//     protocol on stdout and logs on stderr. Example client config:
//     { "command": "/path/to/quant4dad-mcp", "args": ["-config", "/path/to/quant4dad.yaml"] }
//
//   - HTTP (-http): a standalone long-running service over Streamable HTTP / SSE at
//     the /mcp endpoint. The in-container listen port is fixed (taken from
//     config.mcp.addr, i.e. :8080, by default) and the host port is injected by
//     the deployment platform. Clients connect by URL, for example:
//     claude mcp add --transport http quant4dad-kline http://host:port/mcp
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/health"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/mcp"
	"github.com/quant4dad/internal/observability"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

// version is injected at build time: go build -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to quant4dad.yaml")
	httpMode := flag.Bool("http", false, "run as a standalone HTTP (Streamable HTTP / SSE) service instead of stdio")
	probe := flag.String("check-ready", "", "check readiness and exit")
	flag.Parse()
	if *probe != "" {
		os.Exit(health.ExitCode(*probe))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runtime, err := config.Open(ctx, *configPath)
	if err != nil {
		os.Stderr.WriteString("failed to load configuration: " + err.Error() + "\n")
		os.Exit(1)
	}

	defer runtime.Close()
	cfg := runtime.Config

	if *httpMode && cfg.ValidateExternalMCP() != nil {
		os.Stderr.WriteString("external MCP requires a distinct mcp.external_token\n")
		os.Exit(1)
	}

	// Logging writes stderr in both transports, keeping the stdio protocol clean.
	if err := logger.Init(logger.Config{Level: cfg.Log.Level, BaseName: cfg.Log.BaseName, Stderr: true}); err != nil {
		os.Stderr.WriteString("failed to initialize logger: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer logger.Shutdown()
	if err := observability.InitMetrics(); err != nil {
		logger.L().Fatal("initialize metrics failed", zap.Error(err))
	}
	defer observability.ShutdownMetrics()

	if cfg.MCP.AgentToolsEnabled {
		if !*httpMode {
			logger.L().Fatal("full Agent tool mode requires HTTP transport")
		}
		handler, ready, err := newAPIProxy(cfg)
		if err != nil {
			logger.L().Fatal("initialize MCP API proxy failed")
		}
		runHTTPHandler(ctx, monitoredHTTPHandler(handler, ready), cfg)
		return
	}

	// Query-only process: the API owns migrations and seed data.
	db, err := repository.OpenMCPQueryStorage(cfg.MCPStorage())
	if err != nil {
		logger.L().Fatal("open db failed", zap.Error(err))
	}

	sqlDB, err := db.DB()
	if err != nil {
		logger.L().Fatal("database handle unavailable")
	}
	defer sqlDB.Close()

	var barRepo repository.BarRepository
	if cfg.Storage.Backend == config.StorageBackendCSV {
		br, err := repository.OpenCSVBarQueryRepository(cfg.Storage.CSV.BaseDir)
		if err != nil {
			logger.L().Fatal("open csv bar repo failed", zap.Error(err))
		}
		barRepo = br
	} else {
		barRepo = repository.NewGormBarRepository(db)
	}
	instrumentRepo := repository.NewInstrumentRepository(db)
	instrumentSvc := service.NewInstrumentService(instrumentRepo, barRepo)
	eventRepo := repository.NewEventRepository(db)

	// A registry supplies read-only node metadata; no node is executed.
	pipelineRepo := repository.NewPipelineRepository(db)
	pipelineReg := nodes.BuildRegistry(nil, nil)
	pipelineSvc := service.NewPipelineService(pipelineRepo, pipelineReg)

	srv := mcp.NewServer("quant4dad-kline", version)
	mcp.RegisterKlineTools(srv, instrumentSvc)
	mcp.RegisterEventTools(srv, service.NewEventQueryService(eventRepo))
	mcp.RegisterPipelineTools(srv, pipelineSvc)

	if *httpMode {
		runHTTP(ctx, srv, cfg, sqlDB.PingContext)
		return
	}

	logger.L().Info("mcp server started (stdio)", zap.String("version", version),
		zap.String("storage", cfg.Storage.Backend))
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		logger.L().Error("mcp server exited with error", zap.Error(err))
		os.Exit(1)
	}
	logger.L().Info("mcp server stopped")
}

// runHTTP runs the long-running service over the Streamable HTTP transport,
// watching ctx for cancellation to shut down gracefully.
func runHTTP(ctx context.Context, srv *mcp.Server, cfg *config.Config, ready health.Check) {
	runHTTPHandler(ctx, newHTTPHandler(srv, cfg.MCP.ExternalToken, ready), cfg)
}

func runHTTPHandler(ctx context.Context, handler http.Handler, cfg *config.Config) {
	listenAddr := cfg.MCP.Addr

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.L().Error("mcp http shutdown error", zap.Error(err))
		}
	}()

	logger.L().Info("mcp server started (http)",
		zap.String("version", version),
		zap.String("addr", listenAddr),
		zap.String("endpoint", "/mcp"),
		zap.String("storage", cfg.Storage.Backend),
		zap.Bool("auth", true))

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.L().Fatal("mcp http server failed", zap.Error(err))
	}
	// ListenAndServe returns as soon as Shutdown closes its listener; wait for
	// in-flight MCP calls before main tears down metrics and flushes logging.
	<-shutdownDone
	logger.L().Info("mcp server stopped")
}

// Trace is the outer wrapper so the metric middleware and MCP handlers share
// the enriched request. The metric recorder preserves SSE flushing.
func newHTTPHandler(srv *mcp.Server, token string, checks ...health.Check) http.Handler {
	var ready health.Check
	if len(checks) > 0 {
		ready = checks[0]
	}
	return monitoredHTTPHandler(srv.HTTPHandler(token), ready)
}

func monitoredHTTPHandler(handler http.Handler, ready health.Check) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	mux.HandleFunc("GET /livez", health.Ready(nil))
	mux.HandleFunc("GET /healthz", health.Ready(nil))
	mux.HandleFunc("GET /readyz", health.Ready(ready))
	mux.HandleFunc("GET /mcp/readyz", health.Ready(ready))
	mux.Handle("GET /metrics", observability.Handler())
	return logger.TraceMiddleware(observability.Middleware(mux, nil))
}
