package application

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

const sessionTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type sessionTestRuntime struct {
	mu      sync.Mutex
	created map[string]agentbridge.SessionCreated
	calls   int
	lose    bool
	hold    chan struct{}
	entered chan struct{}
	reject  bool
}

func (r *sessionTestRuntime) CreateSession(ctx context.Context, request agentbridge.SessionCreate) (agentbridge.SessionCreated, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	lose, hold, reject := r.lose, r.hold, r.reject
	result, exists := r.created[request.SessionID]
	if !exists {
		result = agentbridge.SessionCreated{SessionID: request.SessionID, DSHSessionID: request.SessionID, Durable: true, Provider: request.Provider, Model: request.Model, Profile: request.Profile, CreatedProfileRevision: request.ProfileRevision, CreatedModelConfigRevision: request.ModelConfigRevision, ProvisionRequestHash: request.ProvisionRequestHash, RuntimeProvenance: agentbridge.Provenance{BridgeProtocol: 1, AdapterVersion: "fixture-v1", Backend: "cordis", DSHVersion: "0.1.2-alpha.5", SessionFormat: 0, EventJournalFormat: 1, SessionBindingFormat: 1}}
		if !reject {
			r.created[request.SessionID] = result
		}
	}
	r.mu.Unlock()
	if reject {
		return agentbridge.SessionCreated{}, &agentbridge.Error{Code: "agent_session_unbound", Status: 409}
	}
	if call == 1 && hold != nil {
		close(r.entered)
		select {
		case <-hold:
		case <-ctx.Done():
			return agentbridge.SessionCreated{}, ctx.Err()
		}
	}
	if lose && call == 1 {
		return agentbridge.SessionCreated{}, errors.New("private-transport-diagnostic")
	}
	return result, nil
}
func (r *sessionTestRuntime) Transcript(_ context.Context, id string, _ agentbridge.TranscriptQuery) (agentbridge.TranscriptPage, error) {
	return agentbridge.TranscriptPage{SessionID: id, SnapshotSeq: "1", Items: []agentbridge.TranscriptItem{}}, nil
}
func (r *sessionTestRuntime) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, len(r.created)
}
func sessionAppFixture(t *testing.T) (*AgentSessionApplication, *service.AgentSessionService, *service.SettingService, *sessionTestRuntime, *gorm.DB) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "session.db") + "?_busy_timeout=5000&_journal_mode=WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&domain.Setting{}, &domain.AgentSessionBinding{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	settings := service.NewSettingService(repository.NewSettingRepository(db))
	enabled := true
	if err = settings.SetLLMProviders(context.Background(), map[string]service.LLMProvider{"fixture": {Type: "openai", DefaultModel: "fixture-model", BaseURL: "http://fixture.invalid/v1", APIKey: "session-fixture-private-key", Agent: &service.AgentModelOptions{Enabled: &enabled, ContextWindow: 8192, MaxOutputTokens: 512}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := settings.GetLLMModelSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := domain.AgentModelProbeRequest{Provider: "fixture", Model: "fixture-model", ModelConfigRevision: snapshot.Revision, ProbeVersion: domain.AgentModelProbeVersion}
	attempt, err := settings.BeginAgentModelProbe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	result := domain.AgentModelProbeResult{Provider: request.Provider, Model: request.Model, ModelConfigRevision: request.ModelConfigRevision, ProbeVersion: request.ProbeVersion, Status: "ready", Reason: "probe_passed", CheckedAtMS: now, ExpiresAtMS: now + 86400000, ContextWindow: 8192, MaxOutputTokens: 512, ContextSource: "explicit-config", ModelTurns: 2, ToolCalls: 1}
	if err = settings.FinishAgentModelProbe(context.Background(), request, attempt, &result); err != nil {
		t.Fatal(err)
	}
	sessions := service.NewAgentSessionService(repository.NewAgentSessionRepository(db))
	runtime := &sessionTestRuntime{created: map[string]agentbridge.SessionCreated{}}
	app, err := NewAgentSessionApplication(settings, sessions, service.NewAgentSessionRuntimeService(runtime), map[string]string{"text_only": sessionTestDigest})
	if err != nil {
		t.Fatal(err)
	}
	return app, sessions, settings, runtime, db
}
func sessionTestCreate() service.AgentSessionCreate {
	return service.AgentSessionCreate{Provider: "fixture", Model: "fixture-model", Profile: "text_only", Title: "原始标题"}
}
func TestAgentSessionCreationIdempotencyAndOwnership(t *testing.T) {
	app, _, settings, runtime, _ := sessionAppFixture(t)
	ctx := context.Background()
	request := sessionTestCreate()
	one, err := app.Create(ctx, "local-user", "client-1", request)
	if err != nil || one.Status != domain.AgentSessionActive {
		t.Fatal(one, err)
	}
	if _, offset := one.CreatedAt.Zone(); offset != 0 || one.CreatedAt.Nanosecond()%1000000 != 0 {
		t.Fatal("session timestamps must use UTC milliseconds")
	}
	title := "新标题"
	archived := true
	if _, err = app.Patch(ctx, "local-user", one.SessionID, service.AgentSessionPatch{Title: &title, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err = settings.PatchLLMProvider(ctx, "fixture", service.LLMProviderPatch{APIKeyUpdate: &service.ModelCredentialUpdate{Action: "revoke"}}, ""); err != nil {
		t.Fatal(err)
	}
	two, err := app.Create(ctx, "local-user", "client-1", request)
	if err != nil || two.SessionID != one.SessionID || two.Title != title || two.Status != domain.AgentSessionArchived {
		t.Fatal(two, err)
	}
	request.Title = "different"
	if _, err = app.Create(ctx, "local-user", "client-1", request); !errors.Is(err, service.ErrAgentSessionConflict) {
		t.Fatal("changed input accepted", err)
	}
	if _, err = app.Create(ctx, "local-user", "new-key", request); !errors.Is(err, service.ErrAgentSessionModel) {
		t.Fatal("revoked model allowed", err)
	}
	if _, err = app.Detail(ctx, "another-owner", one.SessionID, agentbridge.TranscriptQuery{}); !errors.Is(err, service.ErrAgentSessionNotFound) {
		t.Fatal("cross-actor read", err)
	}
	if _, err = app.Patch(ctx, "another-owner", one.SessionID, service.AgentSessionPatch{Title: &title}); !errors.Is(err, service.ErrAgentSessionNotFound) {
		t.Fatal("cross-actor edit", err)
	}
	if calls, created := runtime.counts(); calls != 1 || created != 1 {
		t.Fatal(calls, created)
	}
}
func TestAgentSessionLostAcknowledgementAndRestart(t *testing.T) {
	app, sessions, settings, runtime, db := sessionAppFixture(t)
	runtime.lose = true
	ctx := context.Background()
	one, err := app.Create(ctx, "local-user", "lost-ack", sessionTestCreate())
	if err != nil || one.Status != domain.AgentSessionFailed || strings.Contains(one.ProvisioningErrorCode, "private") {
		t.Fatal(one, err)
	}
	row, err := sessions.Get(ctx, "local-user", one.SessionID)
	if err != nil || row.DSHSessionID != nil {
		t.Fatal("unacknowledged session activated", err)
	}
	archived := true
	if _, err = app.Patch(ctx, "local-user", one.SessionID, service.AgentSessionPatch{Archived: &archived}); !errors.Is(err, service.ErrAgentSessionState) {
		t.Fatal(err)
	}
	fresh := service.NewAgentSessionService(repository.NewAgentSessionRepository(db))
	restarted, err := NewAgentSessionApplication(settings, fresh, service.NewAgentSessionRuntimeService(runtime), map[string]string{"text_only": sessionTestDigest})
	if err != nil {
		t.Fatal(err)
	}
	two, err := restarted.Reconcile(ctx, "local-user", one.SessionID)
	if err != nil || two.SessionID != one.SessionID || two.Status != domain.AgentSessionActive {
		t.Fatal(two, err)
	}
	if calls, created := runtime.counts(); calls != 2 || created != 1 {
		t.Fatal("duplicate materialization", calls, created)
	}
}
func TestAgentSessionConcurrentLeaseAndLateCompletion(t *testing.T) {
	app, _, _, runtime, db := sessionAppFixture(t)
	runtime.hold = make(chan struct{})
	runtime.entered = make(chan struct{})
	runtime.lose = true
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { _, err := app.Create(ctx, "local-user", "concurrent", sessionTestCreate()); done <- err }()
	<-runtime.entered
	two, err := app.Create(ctx, "local-user", "concurrent", sessionTestCreate())
	if err != nil || two.Status != domain.AgentSessionProvisioning {
		t.Fatal(two, err)
	}
	if calls, _ := runtime.counts(); calls != 1 {
		t.Fatal("live lease dispatched twice", calls)
	}
	if err = db.Model(&domain.AgentSessionBinding{}).Where("id = ?", two.SessionID).Update("lease_until_ms", 0).Error; err != nil {
		t.Fatal(err)
	}
	latest, err := app.Reconcile(ctx, "local-user", two.SessionID)
	if err != nil || latest.Status != domain.AgentSessionActive {
		t.Fatal(latest, err)
	}
	close(runtime.hold)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	page, err := app.List(ctx, "local-user", "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != domain.AgentSessionActive {
		t.Fatal(page, err)
	}
}
func TestAgentSessionCommitFailureAndBackgroundRecovery(t *testing.T) {
	app, _, _, runtime, db := sessionAppFixture(t)
	ctx := context.Background()
	if err := db.Exec("CREATE TRIGGER reject_session_active BEFORE UPDATE ON agent_session_binding WHEN NEW.status = 'active' BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Create(ctx, "local-user", "commit-crash", sessionTestCreate()); err == nil {
		t.Fatal("commit failure ignored")
	}
	var row domain.AgentSessionBinding
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != domain.AgentSessionProvisioning || row.DSHSessionID != nil {
		t.Fatal("partial DB activation")
	}
	if err := db.Exec("DROP TRIGGER reject_session_active").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&row).Update("lease_until_ms", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.ReconcileBatch(ctx); err != nil {
		t.Fatal(err)
	}
	detail, err := app.Detail(ctx, "local-user", row.ID, agentbridge.TranscriptQuery{})
	if err != nil || detail.Session.Status != domain.AgentSessionActive || detail.Transcript == nil {
		t.Fatal(detail, err)
	}
	if calls, created := runtime.counts(); calls != 2 || created != 1 {
		t.Fatal(calls, created)
	}
}
func TestAgentSessionPaginationAndActorKeyIsolation(t *testing.T) {
	app, _, _, _, _ := sessionAppFixture(t)
	ctx := context.Background()
	ids := map[string]bool{}
	for _, key := range []string{"Key", "key", "third"} {
		v, err := app.Create(ctx, "Owner", key, sessionTestCreate())
		if err != nil {
			t.Fatal(err)
		}
		ids[v.SessionID] = true
	}
	if _, err := app.Create(ctx, "owner", "Key", sessionTestCreate()); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := app.List(ctx, "Owner", cursor, 1)
		if err != nil || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		id := page.Items[0].SessionID
		if seen[id] || !ids[id] {
			t.Fatal("pagination/actor isolation failed")
		}
		seen[id] = true
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("lost rows")
	}
	if _, err := app.List(ctx, "Owner", "invalid", 20); !errors.Is(err, service.ErrAgentSessionInput) {
		t.Fatal(err)
	}
}
func TestAgentSessionUnboundFailureBackoffAndStop(t *testing.T) {
	app, _, _, runtime, db := sessionAppFixture(t)
	runtime.reject = true
	ctx := context.Background()
	one, err := app.Create(ctx, "local-user", "orphan", sessionTestCreate())
	if err != nil || one.Status != domain.AgentSessionFailed || one.ProvisioningErrorCode != "agent_session_unbound" {
		t.Fatal(one, err)
	}
	if err = app.ReconcileBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if calls, created := runtime.counts(); calls != 1 || created != 0 {
		t.Fatal("unsafe automatic adoption or immediate retry")
	}
	if err = db.Model(&domain.AgentSessionBinding{}).Where("id = ?", one.SessionID).Update("retry_at_ms", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err = app.ReconcileBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if calls, created := runtime.counts(); calls != 2 || created != 0 {
		t.Fatal(calls, created)
	}
	stop := app.StartReconciler()
	stop()
}

func TestAgentSessionCancellationPersistsRecovery(t *testing.T) {
	app, _, _, runtime, _ := sessionAppFixture(t)
	runtime.hold = make(chan struct{})
	runtime.entered = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		value service.AgentSessionView
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := app.Create(ctx, "local-user", "cancelled-request", sessionTestCreate())
		done <- result{value, err}
	}()
	<-runtime.entered
	cancel()
	out := <-done
	if out.err != nil || out.value.Status != domain.AgentSessionFailed || out.value.SessionID == "" {
		t.Fatal("cancelled transport lost recoverable saga response", out)
	}
	repaired, err := app.Reconcile(context.Background(), "local-user", out.value.SessionID)
	if err != nil || repaired.Status != domain.AgentSessionActive {
		t.Fatal(repaired, err)
	}
	if calls, created := runtime.counts(); calls != 2 || created != 1 {
		t.Fatal(calls, created)
	}
}
