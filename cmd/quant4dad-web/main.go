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

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/health"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/observability"
	"github.com/quant4dad/internal/webui"
)

func main() { os.Exit(run()) }
func run() int {
	local := flag.String("config", "", "local complete YAML")
	assets := flag.String("assets", "/app/web", "built static assets")
	probe := flag.String("check-ready", "", "check readiness and exit")
	flag.Parse()
	if *probe != "" {
		return health.ExitCode(*probe)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runtime, err := config.Open(ctx, *local)
	if err != nil {
		os.Stderr.WriteString("web configuration unavailable: " + err.Error() + "\n")
		return 1
	}
	defer runtime.Close()
	if err := logger.Init(logger.Config{Level: runtime.Config.Log.Level}); err != nil {
		return 1
	}
	defer logger.Shutdown()
	if err := observability.InitMetrics(); err != nil {
		return 1
	}
	defer observability.ShutdownMetrics()
	handler, err := webui.New(runtime.Config.Web, *assets)
	if err != nil {
		logger.L().Error("web assets unavailable")
		return 1
	}
	defer handler.Close()
	srv := &http.Server{Addr: runtime.Config.Web.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	logger.L().Info("web server starting")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		stop()
		<-done
		logger.L().Error("web listener failed")
		return 1
	}
	<-done
	return 0
}
