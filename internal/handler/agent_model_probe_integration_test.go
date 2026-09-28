//go:build probeintegration

package handler

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	productprofiles "github.com/quant4dad/agent-runtime/profiles"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/pythonexec"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

type probeFixtureBackend struct {
	current          atomic.Pointer[repository.AgentModelProbeRepository]
	sessions         atomic.Pointer[repository.AgentSessionRuntimeRepository]
	loseSessionReply atomic.Bool
	runs             atomic.Pointer[repository.AgentRunRuntimeRepository]
	loseRunReply     atomic.Bool
	expireEvents     atomic.Bool
}

type probePythonFixture struct{}

func (probePythonFixture) Execute(context.Context, string, []byte) (pythonexec.Result, error) {
	return pythonexec.Result{Status: "succeeded", Stdout: `{"fixture":true}`}, nil
}

func (b *probeFixtureBackend) DecideApproval(ctx context.Context, id string, decision agentbridge.ApprovalDecision) (agentbridge.ApprovalAcknowledged, error) {
	if client := b.runs.Load(); client != nil {
		return client.DecideApproval(ctx, id, decision)
	}
	return agentbridge.ApprovalAcknowledged{}, agentbridge.ErrTransport
}

func (b *probeFixtureBackend) RuntimeCapabilities(ctx context.Context) (agentbridge.Capabilities, error) {
	if client := b.runs.Load(); client != nil {
		return client.RuntimeCapabilities(ctx)
	}
	return agentbridge.Capabilities{}, agentbridge.ErrTransport
}

func (b *probeFixtureBackend) StreamEvents(ctx context.Context, session, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
	if b.expireEvents.Load() {
		return agentbridge.StreamResult{}, &agentbridge.Error{Code: "agent_event_cursor_expired", Status: 410}
	}
	if client := b.runs.Load(); client != nil {
		return client.StreamEvents(ctx, session, id, cursor, callbacks)
	}
	return agentbridge.StreamResult{}, agentbridge.ErrTransport
}

func (b *probeFixtureBackend) Prompt(ctx context.Context, r agentbridge.PromptRequest) (agentbridge.PromptAccepted, error) {
	if client := b.runs.Load(); client != nil {
		value, err := client.Prompt(ctx, r)
		if err == nil && b.loseRunReply.CompareAndSwap(true, false) {
			return agentbridge.PromptAccepted{}, agentbridge.ErrTransport
		}
		return value, err
	}
	return agentbridge.PromptAccepted{}, agentbridge.ErrTransport
}
func (b *probeFixtureBackend) GetRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	if client := b.runs.Load(); client != nil {
		return client.GetRun(ctx, session, id)
	}
	return agentbridge.Run{}, agentbridge.ErrTransport
}
func (b *probeFixtureBackend) CancelRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	if client := b.runs.Load(); client != nil {
		return client.CancelRun(ctx, session, id)
	}
	return agentbridge.Run{}, agentbridge.ErrTransport
}
func (b *probeFixtureBackend) ReconcileRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	if client := b.runs.Load(); client != nil {
		return client.ReconcileRun(ctx, session, id)
	}
	return agentbridge.Run{}, agentbridge.ErrTransport
}

func (b *probeFixtureBackend) ProbeModel(ctx context.Context, r domain.AgentModelProbeRequest) (domain.AgentModelProbeResult, error) {
	if client := b.current.Load(); client != nil {
		return client.ProbeModel(ctx, r)
	}
	return domain.AgentModelProbeResult{}, service.ErrAgentProbeUnavailable
}
func (b *probeFixtureBackend) CreateSession(ctx context.Context, r agentbridge.SessionCreate) (agentbridge.SessionCreated, error) {
	if client := b.sessions.Load(); client != nil {
		value, err := client.CreateSession(ctx, r)
		if err == nil && b.loseSessionReply.CompareAndSwap(true, false) {
			return agentbridge.SessionCreated{}, agentbridge.ErrTransport
		}
		return value, err
	}
	return agentbridge.SessionCreated{}, agentbridge.ErrTransport
}
func (b *probeFixtureBackend) Transcript(ctx context.Context, id string, q agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
	if client := b.sessions.Load(); client != nil {
		return client.Transcript(ctx, id, q)
	}
	return agentbridge.TranscriptPage{}, agentbridge.ErrTransport
}
func TestAgentProbeControlHost(t *testing.T) {
	endpoint := os.Getenv("Q4D_PROBE_MODEL_URL")
	if endpoint == "" {
		t.Fatal("integration host requires synthetic model URL")
	}
	_, db := modelHTTPServer(t)
	if err := db.AutoMigrate(&domain.AgentSessionBinding{}, &domain.AgentRequestBinding{}); err != nil {
		t.Fatal(err)
	}
	settings := service.NewSettingService(repository.NewSettingRepository(db))
	enabled := true
	p0 := os.Getenv("Q4D_P0_FIXTURE") == "1"
	candidateMeter := os.Getenv("Q4D_CANDIDATE_METER_FIXTURE") == "1"
	window := 8192
	if p0 || candidateMeter {
		window = 65536
	}
	err := settings.SetLLMProviders(context.Background(), map[string]service.LLMProvider{"fixture": {Type: "openai", BaseURL: endpoint, APIKey: "probe-fixture-model-secret", DefaultModel: "fixture-model", Agent: &service.AgentModelOptions{Enabled: &enabled, ContextWindow: window, MaxOutputTokens: 512}}})
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true, Bootstrap: config.AgentBootstrapConfig{Enabled: true, ControlToken: bootstrapHTTPControl, MCPToken: bootstrapHTTPMCP, MCPURL: "http://mcp:8081/internal/mcp", CapabilityIssuer: "fixture-issuer", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key)}}, ModelProbe: config.AgentModelProbeConfig{Enabled: true}}}
	cfg.Agent.Sessions = config.AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	digest := cfg.Agent.Sessions.Profiles["text_only"]
	cfg.Agent.ModelProbe.RuntimeURL = "http://runtime"
	cfg.Agent.ModelProbe.BridgeToken = "probe-fixture-bridge-token-0123456789"
	cfg.Agent.Runs = config.AgentRunsConfig{Enabled: true, SigningKeyID: "key-1", SigningPrivateKey: base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32))),
		Manifest: config.AgentRunManifest{Q4DVersion: "0.0.0", AgentImageDigest: digest, AgentRuntimeVersion: "fixture-v1", AdapterVersion: "fixture-v1", DSHVersion: "0.1.2-alpha.5"},
		Profiles: map[string]config.AgentRunProfile{"text_only": {PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: digest, Budgets: agentrunauth.Budgets{MaxTurns: 1, MaxInputTokens: 8192, MaxOutputTokens: 512, WallTimeMS: 30000}}}}
	httpServer := httptest.NewUnstartedServer(nil)
	defer httpServer.Close()
	var catalog *application.ToolCatalogApplication
	var queries atomic.Int64
	var holdTool atomic.Bool
	if os.Getenv("Q4D_RESEARCH_FIXTURE") == "1" || p0 {
		cfg.Agent.Sessions.Profiles = map[string]string{"research": digest}
		p := cfg.Agent.Runs.Profiles["text_only"]
		p.ToolCatalogRevision = ""
		p.Budgets.MaxTurns, p.Budgets.MaxToolCalls, p.Budgets.MaxOutputTokens = 4, 2, 2048
		if candidateMeter {
			p.Budgets.MaxInputTokens = 262144
		}
		cfg.Agent.Runs.Profiles = map[string]config.AgentRunProfile{"research": p}
		if p0 {
			p.Budgets.MaxInputTokens, p.Budgets.MaxToolCalls = 262144, 10
			cfg.Agent.Sessions.Profiles = map[string]string{"research": digest, "strategy_lab": digest, "pipeline_builder": digest}
			cfg.Agent.Runs.Profiles = map[string]config.AgentRunProfile{"research": p, "strategy_lab": p, "pipeline_builder": p}
			if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
				t.Fatal(err)
			}
		}
		for id, p := range cfg.Agent.Runs.Profiles {
			definition := productprofiles.Defaults()[id]
			cfg.Agent.Sessions.Profiles[id] = definition.Revision
			p.PromptBundleDigest, p.SkillsDigest = definition.PromptBundleDigest, definition.SkillsDigest
			cfg.Agent.Runs.Profiles[id] = p
		}
		parent, pathErr := filepath.EvalSymlinks(t.TempDir())
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		cfg.Agent.Gateway = config.AgentGatewayConfig{Enabled: true, ResultDirectory: filepath.Join(parent, "artifacts")}
		cfg.Agent.Bootstrap.MCPURL = "http://" + httpServer.Listener.Addr().String() + "/internal/mcp"
		if err := db.AutoMigrate(&domain.Instrument{}, &domain.Bar{}, &domain.AgentToolAudit{}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			if err := db.Create(&domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: time.Date(2026, 9, i+1, 0, 0, 0, 0, time.UTC), Open: float64(100 + i), High: float64(101 + i), Low: float64(99 + i), Close: float64(100 + i), Volume: 10000}).Error; err != nil {
				t.Fatal(err)
			}
		}
		catalog = application.NewToolCatalogApplication()
		instrument := service.NewInstrumentService(repository.NewInstrumentRepository(db), repository.NewGormBarRepository(db))
		for _, tool := range application.MarketToolDefinitions(instrument) {
			catalog.Register(tool)
		}
		if p0 {
			for _, row := range []any{&domain.Instrument{Code: "sh.600519", Name: "fixture"}, &domain.News{Source: "fixture", ExternalID: "fixture-1", Title: "fixture news", Content: "stored text"}, &domain.Event{EventUID: "fixture-1", PipelineID: 1, Source: "fixture", Status: "passed", FinalPayload: []byte(`{"text":"stored event"}`)}} {
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			pl := service.NewPipelineService(repository.NewPipelineRepository(db), nodes.BuildRegistry(nil, nil))
			cost := service.NewCostService(repository.NewCostRepository(db))
			if err := cost.EnsureDefault(context.Background()); err != nil {
				t.Fatal(err)
			}
			fileRepo, err := repository.NewAgentToolArtifactRepository(filepath.Join(parent, "tmp", "kline"))
			if err != nil {
				t.Fatal(err)
			}
			st := service.NewStrategyService(repository.NewStrategyRepository(db), fileRepo)
			catalog = application.NewAgentP0Catalog(instrument, service.NewIndicatorService(), st, pl, service.NewEventQueryService(repository.NewEventRepository(db)), service.NewAgentCatalogQueryService(repository.NewAgentCatalogQueryRepository(db)), service.NewAgentMutationService(repository.NewAgentMutationRepository(db), st, pl), cost, service.NewAgentKlineFileService(fileRepo, probePythonFixture{}))
			bt := service.NewBacktestService(repository.NewBacktestRepository(db), repository.NewTradeRepository(db), repository.NewEquityRepository(db))
			defer application.NewBacktestApp(bt, st, cost, repository.NewGormBarRepository(db), 1).StartAgentWorker()()
		}
		if err := db.Callback().Query().Before("gorm:query").Register("fixture:research", func(tx *gorm.DB) {
			if tx.Statement.Table != "bar" {
				return
			}
			queries.Add(1)
			if holdTool.Load() {
				<-tx.Statement.Context.Done()
				tx.AddError(tx.Statement.Context.Err())
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	bootstrap, err := application.NewAgentBootstrapApplication(settings, cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	backend := &probeFixtureBackend{}
	sessions := service.NewAgentSessionService(repository.NewAgentSessionRepository(db))
	sessionApp, err := application.NewAgentSessionApplication(settings, sessions, service.NewAgentSessionRuntimeService(backend), cfg.Agent.Sessions.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	probes := application.NewAgentModelProbeApplication(settings, service.NewAgentModelProbeService(backend))
	signer, err := cfg.AgentRunSigner()
	if err != nil {
		t.Fatal(err)
	}
	runs := service.NewAgentRunService(repository.NewAgentRunRepository(db), signer, "key-1")
	runApp, err := application.NewAgentRunApplication(settings, sessions, runs, service.NewAgentRunRuntimeService(backend), cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	handlers := Handlers{Setting: NewSettingHandler(settings), AgentBootstrap: NewAgentBootstrapHandler(bootstrap, bootstrapHTTPControl), AgentModelProbe: NewAgentModelProbeHandler(probes), AgentSession: NewAgentSessionHandler(sessionApp), AgentRun: NewAgentRunHandler(runApp, bootstrapHTTPControl)}
	if catalog != nil {
		artifacts, err := repository.NewAgentToolArtifactRepository(cfg.Agent.Gateway.ResultDirectory)
		if err != nil {
			t.Fatal(err)
		}
		audit := service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
		var approval *service.AgentApprovalService
		if p0 {
			approval = service.NewAgentApprovalService(repository.NewAgentApprovalRepository(db), signer)
			handlers.AgentApproval = NewAgentApprovalHandler(application.NewAgentApprovalApplication(approval, runApp, service.NewAgentRunRuntimeService(backend)))
		}
		handlers.AgentToolResult = NewAgentToolResultHandler(application.NewAgentToolResultApplication(audit, runApp))
		gateway, err := application.NewAgentToolGatewayApplication(catalog, audit, cfg, runApp.ToolAuthority, approval)
		if err != nil {
			t.Fatal(err)
		}
		handlers.AgentGateway = NewAgentToolGatewayHandler(gateway, bootstrapHTTPMCP)
	}
	server := NewServer(cfg, handlers)
	httpServer.Config.Handler = server.engine
	httpServer.Start()
	emit := func(value any) {
		data, _ := sonic.Marshal(value)
		_, _ = os.Stdout.Write(append(append([]byte("Q4D_PROBE\t"), data...), '\n'))
	}
	emit(map[string]any{"event": "ready", "url": httpServer.URL})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command struct {
			ID               int    `json:"id"`
			URL              string `json:"url"`
			LoseSessionReply bool   `json:"lose_session_reply"`
			LoseRunReply     bool   `json:"lose_run_reply"`
			ExpireEvents     bool   `json:"expire_events"`
			ResearchStatus   bool   `json:"research_status"`
			HoldTool         bool   `json:"hold_tool"`
			ReconcileRuns    bool   `json:"reconcile_runs"`
		}
		if sonic.Unmarshal(scanner.Bytes(), &command) != nil {
			t.Fatal("invalid fixture command")
		}
		if command.ReconcileRuns {
			_, err := runApp.ReconcileBatch(context.Background(), "")
			var count int64
			if e := db.Model(&domain.AgentRequestBinding{}).Where("message_id IS NOT NULL").Count(&count).Error; e != nil {
				t.Fatal(e)
			}
			emit(map[string]any{"id": command.ID, "failed": err != nil, "acknowledged": count})
			continue
		}
		if command.HoldTool {
			holdTool.Store(true)
			emit(map[string]any{"id": command.ID})
			continue
		}
		if command.ResearchStatus {
			var rows []domain.AgentToolAudit
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			emit(map[string]any{"id": command.ID, "queries": queries.Load(), "audits": rows})
			continue
		}
		if command.LoseSessionReply {
			backend.loseSessionReply.Store(true)
			emit(map[string]any{"id": command.ID, "connected": true})
			continue
		}
		if command.LoseRunReply {
			backend.loseRunReply.Store(true)
			emit(map[string]any{"id": command.ID, "connected": true})
			continue
		}
		if command.ExpireEvents {
			backend.expireEvents.Store(true)
			emit(map[string]any{"id": command.ID, "connected": true})
			continue
		}
		client, err := agentbridge.New(agentbridge.Config{BaseURL: command.URL, Token: "probe-fixture-bridge-token-0123456789", HeaderTimeout: 17 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		backend.current.Store(repository.NewAgentModelProbeRepository(client))
		backend.sessions.Store(repository.NewAgentSessionRuntimeRepository(client))
		backend.runs.Store(repository.NewAgentRunRuntimeRepository(client))
		emit(map[string]any{"id": command.ID, "connected": true})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal("fixture control read failed")
	}
}
