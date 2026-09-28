package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/handler"
	"github.com/quant4dad/internal/health"
	"github.com/quant4dad/internal/llm"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/mcp"
	"github.com/quant4dad/internal/notify"
	"github.com/quant4dad/internal/observability"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/pythonexec"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
	"github.com/quant4dad/internal/repository/datasource"
	"github.com/quant4dad/internal/service"

	// The mount point for data source implementations. The providers package holds
	// no implementation itself: blank-importing your own provider subpackage from
	// its import block is enough for the build to pick it up, with no change to this
	// file. See internal/repository/datasource/README.md.
	_ "github.com/quant4dad/internal/repository/datasource/providers"
)

func main() {
	configPath := flag.String("config", "", "path to quant4dad.yaml")
	probe := flag.String("check-ready", "", "check readiness and exit")
	migration := flag.String("migration", "", "one-off check, apply or compatible")
	skipMigration := flag.Bool("skip-migration", false, "check the existing schema without applying migrations")
	skipSeed := flag.Bool("skip-seed", false, "do not create default cost settings")
	noBackground := flag.Bool("no-background", false, "disable scheduled collection, scans and Agent workers for inspection")
	setup := flag.String("setup", "", "initialize standalone configuration in this directory and exit")
	flag.Parse()
	if *probe != "" {
		os.Exit(health.ExitCode(*probe))
	}
	if *setup != "" {
		os.Exit(runSetup(*setup))
	}
	if *migration != "" {
		os.Exit(runMigration(*configPath, *migration))
	}
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runtime, err := config.Open(runCtx, *configPath)
	if err != nil {
		os.Stderr.WriteString("failed to load configuration: " + err.Error() + "\n")
		os.Exit(1)
	}

	defer runtime.Close()
	cfg := runtime.Config

	if err := logger.Init(logger.Config{
		Level:    cfg.Log.Level,
		BaseName: cfg.Log.BaseName,
	}); err != nil {
		os.Stderr.WriteString("failed to initialize logger: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer logger.Shutdown()

	if err := observability.InitMetrics(); err != nil {
		logger.L().Fatal("initialize metrics failed", zap.Error(err))
	}
	defer observability.ShutdownMetrics()

	logger.L().Info("config loaded",
		zap.String("addr", cfg.Server.Addr),
		zap.String("storage", cfg.Storage.Backend),
		zap.Bool("auth_enabled", cfg.Security.Token != ""),
		zap.Bool("agent_enabled", cfg.Agent.Enabled),
		zap.Bool("agent_bootstrap_enabled", cfg.Agent.Bootstrap.Enabled),
		zap.Bool("archive_enabled", cfg.Archive.Enabled))

	// 1) Storage: metadata DB always + Bar repo (gorm or csv).
	db, err := repository.Open(cfg.Storage)
	if err != nil {
		logger.L().Fatal("open db failed", zap.Error(err))
	}
	sqlDB, err := db.DB()
	if err != nil {
		logger.L().Fatal("database handle unavailable")
	}
	defer sqlDB.Close()
	if !*skipMigration {
		if err := repository.Migrate(db, cfg.Storage.Backend); err != nil {
			logger.L().Fatal("migrate failed")
		}
	} else if ready, err := repository.CheckSchema(db, cfg.Storage.Backend); err != nil || !ready {
		logger.L().Fatal("database schema requires the release migration")
	}

	var barRepo repository.BarRepository
	if cfg.Storage.Backend == config.StorageBackendCSV {
		br, err := repository.NewCSVBarRepository(cfg.Storage.CSV.BaseDir)
		if err != nil {
			logger.L().Fatal("open csv bar repo failed", zap.Error(err))
		}
		barRepo = br
	} else {
		barRepo = repository.NewGormBarRepository(db)
	}

	// 2) Repository layer.
	instrumentRepo := repository.NewInstrumentRepository(db)
	strategyRepo := repository.NewStrategyRepository(db)
	costRepo := repository.NewCostRepository(db)
	backtestRepo := repository.NewBacktestRepository(db)
	tradeRepo := repository.NewTradeRepository(db)
	equityRepo := repository.NewEquityRepository(db)
	datasyncRepo := repository.NewDataSyncRepository(db)
	coverageRepo := repository.NewDataCoverageRepository(db)
	pipelineRepo := repository.NewPipelineRepository(db)
	eventRepo := repository.NewEventRepository(db)
	settingRepo := repository.NewSettingRepository(db)
	newsRepo := repository.NewNewsRepository(db)
	newsSubRepo := repository.NewNewsSubscriptionRepository(db)

	if !*skipSeed {
		if err := costRepo.EnsureDefault(context.Background()); err != nil {
			logger.L().Fatal("seed default cost failed", zap.Error(err))
		}
	}

	// 3) Service layer.
	instrumentSvc := service.NewInstrumentService(instrumentRepo, barRepo)
	toolsEnabled := cfg.Agent.Gateway.Enabled || cfg.MCP.AgentToolsEnabled
	resultDirectory := cfg.MCP.ToolResultDirectory()
	if cfg.Agent.Gateway.Enabled {
		resultDirectory = cfg.Agent.Gateway.ResultDirectory
	}
	var validationFiles *repository.AgentToolArtifactRepository
	if toolsEnabled {
		validationFiles, err = repository.NewAgentToolArtifactRepository(filepath.Join(resultDirectory, "tmp", "strategy-validation"))
		if err != nil {
			logger.L().Fatal("initialize strategy validation storage failed", zap.Error(err))
		}
	}
	strategySvc := service.NewStrategyService(strategyRepo, validationFiles)
	costSvc := service.NewCostService(costRepo)
	backtestSvc := service.NewBacktestService(backtestRepo, tradeRepo, equityRepo)
	datasyncSvc := service.NewDataSyncService(datasyncRepo)
	indicatorSvc := service.NewIndicatorService()

	// Event pipelines: LLM provider config lives in the setting table (editable from
	// the UI), so a DynamicFactory reads the latest config on every resolve, feeding
	// the node registry and then the service and application layers.
	settingSvc := service.NewSettingService(settingRepo)
	llmResolver := llm.NewDynamicFactory(func(ctx context.Context) (map[string]llm.ProviderConfig, error) {
		providers, err := settingSvc.GetLLMProviders(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]llm.ProviderConfig, len(providers))
		for name, p := range providers {
			out[name] = llm.ProviderConfig{
				Type:         p.Type,
				BaseURL:      p.BaseURL,
				APIKey:       p.APIKey,
				DefaultModel: p.DefaultModel,
			}
		}
		return out, nil
	})
	// Delivery channel (email / Feishu) config also lives in the setting table and is
	// editable from the UI, so a DynamicFactory builds the sender from the latest
	// config on every resolve.
	notifyResolver := notify.NewDynamicFactory(func(ctx context.Context) (notify.ChannelConfigs, error) {
		var out notify.ChannelConfigs
		email, err := settingSvc.GetEmailSetting(ctx)
		if err != nil {
			return out, err
		}
		if email.Host != "" {
			out.Email = &notify.EmailConfig{
				Host:      email.Host,
				Port:      email.Port,
				Username:  email.Username,
				Password:  email.Password,
				From:      email.From,
				UseSSL:    email.UseSSL,
				DefaultTo: email.DefaultTo,
			}
		}
		feishu, err := settingSvc.GetFeishuSetting(ctx)
		if err != nil {
			return out, err
		}
		if feishu.WebhookURL != "" {
			out.Feishu = &notify.FeishuConfig{WebhookURL: feishu.WebhookURL, Secret: feishu.Secret}
		}
		return out, nil
	})
	pipelineReg := nodes.BuildRegistry(llmResolver, notifyResolver)
	pipelineSvc := service.NewPipelineService(pipelineRepo, pipelineReg)

	// 4) Datasource client. Implementations are registered by the providers package's
	// blank imports. With none registered, New returns the placeholder and the service
	// starts as usual — backtests, pipelines and queries over stored data don't depend
	// on fetching — and only triggering a sync reports ErrNoProvider.
	dsClient, err := datasource.New(cfg.Datasource)
	if err != nil {
		logger.L().Fatal("init datasource failed", zap.Error(err))
	}
	if providers := datasource.Providers(); len(providers) == 0 {
		logger.L().Warn("no datasource provider registered: market-data sync is unavailable, " +
			"see internal/repository/datasource/README.md to plug one in")
	} else {
		logger.L().Info("datasource ready",
			zap.String("provider", dsClient.Name()), zap.Strings("registered", providers))
	}

	// 5) Application layer.
	backtestApp := application.NewBacktestApp(backtestSvc, strategySvc, costSvc, barRepo, cfg.Backtest.WorkerPool)
	datasyncApp := application.NewDataSyncApp(cfg.Datasource, datasyncSvc, instrumentSvc, barRepo, dsClient)
	coverageScanner := application.NewCoverageScanner(coverageRepo, instrumentRepo)
	// In CSV mode bars never reach SQL, so there is nothing to aggregate over; every
	// other backend gets the periodic scan.
	var stopCoverageCron context.CancelFunc
	if !*noBackground && cfg.Storage.Backend != config.StorageBackendCSV {
		stopCoverageCron = coverageScanner.StartCron(0)
	}

	autoSync, err := application.NewAutoSyncScheduler(datasyncApp, cfg.AutoSync.Enabled, cfg.AutoSync.DailyTime)
	if err != nil {
		logger.L().Fatal("init auto sync scheduler failed", zap.Error(err))
	}
	var stopAutoSync func()
	if !*noBackground {
		stopAutoSync = autoSync.Start()
	}

	pipelineApp := application.NewPipelineApp(pipelineRepo, eventRepo, pipelineReg)

	// News collection: source fetchers feeding the collection scheduler, which polls
	// per source config, stores deduplicated items, and feeds subscribed pipelines.
	// The collector's global config (master switch + base tick) and each source's
	// config live in the setting table and are changeable at runtime.
	//
	// The usable source names come from the registry, handed to the service layer once
	// during wiring for subscription validation and config defaults. This project
	// ships no collection implementation, so with none registered the list is empty:
	// the collector still runs but idles, and the UI's source list is empty.
	newsSources := datasource.NewsSources()
	service.SetKnownNewsSources(datasource.NewsSourceNames())
	if len(newsSources) == 0 {
		logger.L().Warn("no news source registered: the collector will idle, " +
			"see internal/repository/datasource/README.md to plug one in")
	} else {
		logger.L().Info("news sources ready", zap.Strings("sources", datasource.NewsSourceNames()))
	}
	newsSvc := service.NewNewsService(newsRepo, settingRepo, cfg.News.Enabled, cfg.News.BaseIntervalSeconds)
	newsCollector := application.NewNewsCollector(
		newsSources, newsRepo, newsSubRepo, newsSvc, pipelineApp,
	)
	var stopNews func()
	if !*noBackground {
		stopNews = newsCollector.Start()
	}

	// Hot/cold tiering for the three event tables: at the scheduled time, expired whole
	// days are exported to OSS and then deleted from MySQL. The news layer is not
	// archived yet. As long as a bucket is configured we build the scheduler and
	// register the manual trigger endpoint, which makes it easy to verify OSS
	// connectivity before going live; enabled only decides whether the daily cron
	// runs — Start() is a no-op when enabled=false.
	var (
		archiver    *application.ArchiveScheduler
		stopArchive context.CancelFunc
	)
	if cfg.Archive.OSS.Bucket != "" {
		cold, err := coldstore.NewOSSStore(cfg.Archive.OSS)
		if err != nil {
			logger.L().Fatal("init archive coldstore failed", zap.Error(err))
		}
		archiver, err = application.NewArchiveScheduler(eventRepo, cold, cfg.Archive)
		if err != nil {
			logger.L().Fatal("init archive scheduler failed", zap.Error(err))
		}
		if !*noBackground {
			stopArchive = archiver.Start()
		}
	}

	// 6) Handlers. The UI has moved to its own React + Go Web container (see web/), so
	// the backend serves only the API.
	handlers := handler.Handlers{
		Readiness:  sqlDB.PingContext,
		Strategy:   handler.NewStrategyHandler(strategySvc),
		Indicator:  handler.NewIndicatorHandler(indicatorSvc),
		Instrument: handler.NewInstrumentHandler(instrumentSvc),
		Cost:       handler.NewCostHandler(costSvc),
		Backtest:   handler.NewBacktestHandler(backtestSvc, backtestApp),
		DataSync:   handler.NewDataSyncHandler(datasyncSvc, datasyncApp, coverageScanner, autoSync),
		Pipeline:   handler.NewPipelineHandler(pipelineSvc, pipelineApp),
		Setting:    handler.NewSettingHandler(settingSvc),
		News:       handler.NewNewsHandler(newsSvc, newsCollector),
	}
	if archiver != nil {
		handlers.Archive = handler.NewArchiveHandler(archiver)
	}
	var agentCatalog *application.ToolCatalogApplication
	var stopAgentBacktests func()
	var klineFiles *service.AgentKlineFileService
	var audit *service.AgentToolAuditService
	var stopAgentTools func()
	if toolsEnabled {
		querySvc := service.NewAgentCatalogQueryService(repository.NewAgentCatalogQueryRepository(db))
		mutationSvc := service.NewAgentMutationService(repository.NewAgentMutationRepository(db), strategySvc, pipelineSvc)
		klineRepository, err := repository.NewAgentToolArtifactRepository(filepath.Join(resultDirectory, "tmp", "kline"))
		if err != nil {
			logger.L().Fatal("initialize agent K-line temporary storage failed", zap.Error(err))
		}
		python, err := pythonexec.New(runCtx, pythonexec.DefaultDirectory())
		if err != nil {
			logger.L().Warn("isolated Python runtime unavailable", zap.Error(err))
			klineFiles = service.NewAgentKlineFileService(klineRepository)
		} else {
			defer python.Close()
			klineFiles = service.NewAgentKlineFileService(klineRepository, python)
			logger.L().Info("isolated Python runtime ready", zap.String("version", pythonexec.Version))
		}
		agentCatalog = application.NewAgentP0Catalog(instrumentSvc, indicatorSvc, strategySvc, pipelineSvc, service.NewEventQueryService(eventRepo), querySvc, mutationSvc, costSvc, klineFiles)
		if !*noBackground {
			stopAgentBacktests = backtestApp.StartAgentWorker()
		}
		artifacts, err := repository.NewAgentToolArtifactRepository(resultDirectory)
		if err != nil {
			logger.L().Fatal("initialize tool artifacts failed", zap.Error(err))
		}
		audit = service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
		if !*noBackground {
			stopAgentTools = application.StartAgentToolMaintenance(audit, klineFiles, strategySvc)
		}
	}
	if cfg.Agent.Bootstrap.Enabled {
		bootstrap, err := application.NewAgentBootstrapApplication(settingSvc, cfg, agentCatalog)
		if err != nil {
			logger.L().Fatal("initialize agent bootstrap failed")
		}
		handlers.AgentBootstrap = handler.NewAgentBootstrapHandler(bootstrap, cfg.Agent.Bootstrap.ControlToken)
	}

	var stopAgentSessions, stopAgentRuns, stopAgentTitles func()
	if cfg.Agent.ModelProbe.Enabled {
		if cfg.ValidateAgentModelProbe() != nil {
			logger.L().Fatal("initialize agent model probe failed")
		}
		client, err := agentbridge.New(agentbridge.Config{BaseURL: cfg.Agent.ModelProbe.RuntimeURL, Token: cfg.Agent.ModelProbe.BridgeToken, HeaderTimeout: 17 * time.Second})
		if err != nil {
			logger.L().Fatal("initialize agent model probe client failed")
		}
		defer client.CloseIdleConnections()
		probeSvc := service.NewAgentModelProbeService(repository.NewAgentModelProbeRepository(client))
		handlers.AgentModelProbe = handler.NewAgentModelProbeHandler(application.NewAgentModelProbeApplication(settingSvc, probeSvc))
		if cfg.Agent.Sessions.Enabled {
			if cfg.ValidateAgentSessions() != nil {
				logger.L().Fatal("initialize agent sessions failed")
			}
			sessionSvc := service.NewAgentSessionService(repository.NewAgentSessionRepository(db))
			runtimeSvc := service.NewAgentSessionRuntimeService(repository.NewAgentSessionRuntimeRepository(client))
			sessionApp, err := application.NewAgentSessionApplication(settingSvc, sessionSvc, runtimeSvc, cfg.Agent.Sessions.Profiles)
			if err != nil {
				logger.L().Fatal("initialize agent session application failed")
			}
			handlers.AgentSession = handler.NewAgentSessionHandler(sessionApp)
			if !*noBackground {
				stopAgentSessions = sessionApp.StartReconciler()
			}
			if cfg.Agent.Runs.Enabled {
				signer, err := cfg.AgentRunSigner()
				if err != nil {
					logger.L().Fatal("initialize agent run signer failed")
				}
				runSvc := service.NewAgentRunService(repository.NewAgentRunRepository(db), signer, cfg.Agent.Runs.SigningKeyID)
				runRuntime := service.NewAgentRunRuntimeService(repository.NewAgentRunRuntimeRepository(client))
				runApp, err := application.NewAgentRunApplication(settingSvc, sessionSvc, runSvc, runRuntime, cfg, agentCatalog)
				if err != nil {
					logger.L().Fatal("initialize agent run application failed")
				}
				if cfg.Agent.Runs.ReleaseFile != "" {
					runApp.SetReleaseSource(service.NewAgentReleaseService(repository.NewAgentReleaseRepository(cfg.Agent.Runs.ReleaseFile,
						cfg.Agent.Runs.ReleasePublicKey, cfg.Agent.Runs.Manifest.Q4DVersion, cfg.Agent.Runs.AllowEdge)))
				}
				handlers.AgentRun = handler.NewAgentRunHandler(runApp, cfg.Agent.Bootstrap.ControlToken)
				if !*noBackground {
					stopAgentRuns = runApp.StartReconciler()
				}
				if !*noBackground {
					stopAgentTitles = application.NewAgentSessionTitleApplication(sessionSvc, runtimeSvc, settingSvc, llmResolver).StartWorker()
				}
				if cfg.Agent.Gateway.Enabled {
					if cfg.ValidateAgentGateway() != nil {
						logger.L().Fatal("initialize agent tool gateway failed")
					}
					handlers.AgentToolResult = handler.NewAgentToolResultHandler(application.NewAgentToolResultApplication(audit, runApp))
					approval := service.NewAgentApprovalService(repository.NewAgentApprovalRepository(db), signer)
					handlers.AgentApproval = handler.NewAgentApprovalHandler(application.NewAgentApprovalApplication(approval, runApp, runRuntime))
					gateway, err := application.NewAgentToolGatewayApplication(agentCatalog, audit, cfg, runApp.ToolAuthority, approval)
					if err != nil {
						logger.L().Fatal("initialize agent tool gateway failed")
					}
					handlers.AgentGateway = handler.NewAgentToolGatewayHandler(gateway, cfg.Agent.Bootstrap.MCPToken)
				}
			}
		}
	}
	if cfg.MCP.AgentToolsEnabled {
		signer, err := cfg.MCPReceiptSigner()
		if err != nil {
			logger.L().Fatal("initialize external MCP signing key failed")
		}
		approval := service.NewAgentApprovalService(repository.NewAgentApprovalRepository(db), signer)
		sessions, err := service.NewExternalMCPSessionService(repository.NewExternalMCPSessionRepository(db), 24*time.Hour, 10000)
		if err != nil {
			logger.L().Fatal("initialize external MCP sessions failed")
		}
		external, err := application.NewExternalMCPApplication(agentCatalog, audit, approval, sessions, true)
		if err != nil {
			logger.L().Fatal("initialize external MCP catalog failed")
		}
		handlers.ExternalMCP = mcp.ExternalHTTPHandler(external, cfg.MCP.ExternalToken)
	}
	srv := handler.NewServer(cfg, handlers)

	go func() {
		logger.L().Info("server starting", zap.String("addr", cfg.Server.Addr))
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Fatal("server exited", zap.Error(err))
		}
	}()

	<-runCtx.Done()

	logger.L().Info("server shutting down")
	if stopAgentBacktests != nil {
		stopAgentBacktests()
	}
	if stopAgentTools != nil {
		stopAgentTools()
	}
	if stopAgentRuns != nil {
		stopAgentRuns()
	}
	if stopAgentTitles != nil {
		stopAgentTitles()
	}
	if stopAgentSessions != nil {
		stopAgentSessions()
	}
	if stopCoverageCron != nil {
		stopCoverageCron()
	}
	if stopAutoSync != nil {
		stopAutoSync()
	}
	if stopNews != nil {
		stopNews()
	}
	if stopArchive != nil {
		stopArchive()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.L().Error("server shutdown error", zap.Error(err))
	}
}
