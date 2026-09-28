package application

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

type runTestRuntime struct {
	mu                                        sync.Mutex
	runs                                      map[string]agentbridge.Run
	tokens                                    []string
	lose, beforeDelivery, unavailable, badAck bool
	verify                                    *agentrunauth.Verifier
	stream                                    func(context.Context, string, string, string, agentbridge.StreamCallbacks) (agentbridge.StreamResult, error)
}

func (r *runTestRuntime) StreamEvents(ctx context.Context, session, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
	if r.stream != nil {
		return r.stream(ctx, session, id, cursor, callbacks)
	}
	return agentbridge.StreamResult{}, agentbridge.ErrTransport
}

func (r *runTestRuntime) Prompt(ctx context.Context, p agentbridge.PromptRequest) (agentbridge.PromptAccepted, error) {
	if _, err := r.verify.Verify(ctx, p.RunCapability, agentrunauth.Binding{SessionID: p.SessionID, RunID: p.RunID, ClientRequestID: p.ClientRequestID, RequestHash: p.RequestHash, EnvelopeDigest: p.ExecutionEnvelopeDigest}, time.Now()); err != nil {
		return agentbridge.PromptAccepted{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, p.RunCapability)
	if r.beforeDelivery {
		r.beforeDelivery = false
		return agentbridge.PromptAccepted{}, agentbridge.ErrTransport
	}
	v, exists := r.runs[p.RunID]
	if !exists {
		v = agentbridge.Run{SessionID: p.SessionID, RunID: p.RunID, MessageID: "message-" + p.RunID, ExecutionEnvelopeDigest: p.ExecutionEnvelopeDigest, Durable: true, State: "completed", Terminal: true, LastEventID: "9007199254740993"}
		r.runs[p.RunID] = v
	}
	if r.lose {
		r.lose = false
		return agentbridge.PromptAccepted{}, agentbridge.ErrTransport
	}
	return agentbridge.PromptAccepted{RunID: p.RunID, MessageID: v.MessageID, Durable: !r.badAck, State: "accepted"}, nil
}
func (r *runTestRuntime) GetRun(_ context.Context, _, id string) (agentbridge.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unavailable {
		return agentbridge.Run{}, agentbridge.ErrTransport
	}
	if v, ok := r.runs[id]; ok {
		return v, nil
	}
	return agentbridge.Run{}, &agentbridge.Error{Code: "agent_run_not_found", Status: 404}
}
func (r *runTestRuntime) CancelRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	return r.GetRun(ctx, session, id)
}
func (r *runTestRuntime) ReconcileRun(ctx context.Context, session, id string) (agentbridge.Run, error) {
	v, err := r.GetRun(ctx, session, id)
	if err == nil && v.State == "recovering" {
		v.State, v.Terminal = "interrupted", true
		r.mu.Lock()
		r.runs[id] = v
		r.mu.Unlock()
	}
	return v, err
}

func TestAgentRunBackgroundReconcileDoesNotRedispatch(t *testing.T) {
	app, _, _, runtime, db, id := runAppFixture(t)
	runtime.lose = true
	sent, err := app.Send(context.Background(), "local-user", id, runTestMessage())
	if err == nil || sent.Durable {
		t.Fatal("expected lost reply")
	}
	runtime.mu.Lock()
	v := runtime.runs[sent.RunID]
	v.State = "recovering"
	v.Terminal = false
	runtime.runs[sent.RunID] = v
	runtime.mu.Unlock()
	if _, err = app.Reconcile(context.Background(), "another-user", sent.RunID); !errors.Is(err, service.ErrAgentRunNotFound) {
		t.Fatal(err)
	}
	if after, err := app.ReconcileBatch(context.Background(), ""); err != nil || after != "" {
		t.Fatal(after, err)
	}
	var row domain.AgentRequestBinding
	if err := db.First(&row, "id = ?", sent.RunID).Error; err != nil || row.MessageID == nil {
		t.Fatal(row, err)
	}
	if len(runtime.tokens) != 1 || runtime.runs[sent.RunID].State != "interrupted" {
		t.Fatal("reconciliation reissued work")
	}
	input := runTestMessage()
	input.ClientRequestID = "not-delivered"
	runtime.beforeDelivery = true
	if _, err = app.Send(context.Background(), "local-user", id, input); err == nil {
		t.Fatal("expected missing delivery")
	}
	_, _ = app.ReconcileBatch(context.Background(), "")
	if len(runtime.tokens) != 2 {
		t.Fatal("background resent missing prompt")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = app.ReconcileBatch(ctx, ""); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}
func runTestConfig() *config.Config {
	private := ed25519.NewKeyFromSeed(make([]byte, 32))
	return &config.Config{Security: config.SecurityConfig{Token: "fixture-login"}, Agent: config.AgentConfig{Enabled: true,
		Bootstrap:  config.AgentBootstrapConfig{Enabled: true, ControlToken: "fixture-control-token-0123456789-abcdef", MCPToken: "fixture-mcp-token-0123456789-abcdef", MCPURL: "http://mcp/internal/mcp", CapabilityIssuer: "fixture-issuer", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(private[32:])}},
		ModelProbe: config.AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: "fixture-bridge-token-0123456789-abcdef"},
		Sessions:   config.AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": sessionTestDigest}},
		Runs: config.AgentRunsConfig{Enabled: true, SigningKeyID: "key-1", SigningPrivateKey: base64.RawURLEncoding.EncodeToString(private),
			Manifest: config.AgentRunManifest{Q4DVersion: "0.0.0", AgentImageDigest: sessionTestDigest, AgentRuntimeVersion: "fixture-v1", AdapterVersion: "fixture-v1", DSHVersion: "0.1.2-alpha.5"},
			Profiles: map[string]config.AgentRunProfile{"text_only": {PromptBundleDigest: sessionTestDigest, SkillsDigest: sessionTestDigest, ToolCatalogRevision: sessionTestDigest, Budgets: agentrunauth.Budgets{MaxTurns: 1, MaxToolCalls: 0, MaxInputTokens: 8192, MaxOutputTokens: 512, WallTimeMS: 30000}}}}}}
}
func runAppFixture(t *testing.T) (*AgentRunApplication, *AgentSessionApplication, *service.SettingService, *runTestRuntime, *gorm.DB, string) {
	t.Helper()
	sessionApp, sessions, settings, _, db := sessionAppFixture(t)
	if err := db.AutoMigrate(&domain.AgentRequestBinding{}); err != nil {
		t.Fatal(err)
	}
	cfg := runTestConfig()
	signer, err := cfg.AgentRunSigner()
	if err != nil {
		t.Fatal(err)
	}
	runs := service.NewAgentRunService(repository.NewAgentRunRepository(db), signer, cfg.Agent.Runs.SigningKeyID)
	runtime := &runTestRuntime{runs: map[string]agentbridge.Run{}}
	app, err := NewAgentRunApplication(settings, sessions, runs, service.NewAgentRunRuntimeService(runtime), cfg)
	if err != nil {
		t.Fatal(err)
	}
	public := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	runtime.verify, err = agentrunauth.NewVerifier("fixture-issuer", agentrunauth.RuntimeAudience, map[string]ed25519.PublicKey{"key-1": public}, func(ctx context.Context, c agentrunauth.Claims) (bool, error) {
		expected, err := app.Authorization(ctx, c.Envelope.RunID)
		return err == nil && expected.JTI == c.JTI, err
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessionApp.Create(context.Background(), "local-user", "session", sessionTestCreate())
	if err != nil {
		t.Fatal(err)
	}
	return app, sessionApp, settings, runtime, db, session.SessionID
}
func runTestMessage() service.AgentRunMessage {
	return service.AgentRunMessage{ClientRequestID: "client-run-1", Content: []agentbridge.TextContent{{Type: "text", Text: "请分析 🐉 <private-prompt-marker>\n保持原文"}}}
}
func TestAgentRunIdempotencyProjectionAndPolicy(t *testing.T) {
	app, sessions, _, runtime, db, id := runAppFixture(t)
	ctx := context.Background()
	input := runTestMessage()
	one, err := app.Send(ctx, "local-user", id, input)
	if err != nil || !one.Durable || one.Status != "accepted" {
		t.Fatal(one, err)
	}
	projection, err := app.Get(ctx, "local-user", one.RunID)
	if err != nil || projection.LastEventID != "9007199254740993" || projection.State != "completed" {
		t.Fatal(projection, err)
	}
	if _, err = app.Get(ctx, "other-owner", one.RunID); !errors.Is(err, service.ErrAgentRunNotFound) {
		t.Fatal("actor read", err)
	}
	if _, err = app.Cancel(ctx, "other-owner", one.RunID); !errors.Is(err, service.ErrAgentRunNotFound) {
		t.Fatal("actor cancel", err)
	}
	archived := true
	if _, err = sessions.Patch(ctx, "local-user", id, service.AgentSessionPatch{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	two, err := app.Send(ctx, "local-user", id, input)
	if err != nil || two.RunID != one.RunID || two.Status != "completed" {
		t.Fatal(two, err)
	}
	if _, err = app.Authorization(ctx, one.RunID); err != nil {
		t.Fatal("archive implicitly revoked existing work", err)
	}
	input.Content[0].Text = "different"
	if _, err = app.Send(ctx, "local-user", id, input); !errors.Is(err, service.ErrAgentRunConflict) {
		t.Fatal(err)
	}
	input.ClientRequestID = "new"
	if _, err = app.Send(ctx, "local-user", id, input); !errors.Is(err, service.ErrAgentSessionState) {
		t.Fatal(err)
	}
	var rows []domain.AgentRequestBinding
	if err = db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].MessageID == nil {
		t.Fatal(rows, err)
	}
	raw, _ := sonic.Marshal(rows)
	for _, private := range []string{"private-prompt-marker", runtime.tokens[0], runTestConfig().Agent.Runs.SigningPrivateKey} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private content persisted")
		}
	}
	if len(runtime.tokens) != 1 || len(runtime.runs) != 1 {
		t.Fatal("duplicate delivery")
	}
}
func TestAgentRunLostReplyCommitFailureAndRecovery(t *testing.T) {
	for _, failure := range []string{"lost-reply", "commit-failure", "non-durable"} {
		t.Run(failure, func(t *testing.T) {
			app, _, _, runtime, db, id := runAppFixture(t)
			ctx := context.Background()
			runtime.lose = failure == "lost-reply"
			runtime.badAck = failure == "non-durable"
			if failure == "commit-failure" {
				if err := db.Exec("CREATE TRIGGER reject_run_ack BEFORE UPDATE ON agent_request_binding WHEN NEW.message_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
					t.Fatal(err)
				}
			}
			one, err := app.Send(ctx, "local-user", id, runTestMessage())
			if err == nil || one.Durable || one.RunID == "" {
				t.Fatal("unconfirmed returned success", one, err)
			}
			row, err := app.runs.Get(ctx, "local-user", one.RunID)
			if err != nil || row.MessageID != nil {
				t.Fatal(err)
			}
			if failure == "commit-failure" {
				if err = db.Exec("DROP TRIGGER reject_run_ack").Error; err != nil {
					t.Fatal(err)
				}
			}
			// Reconstruct the service to prove recovery uses the database, not memory.
			signer, _ := runTestConfig().AgentRunSigner()
			app.runs = service.NewAgentRunService(repository.NewAgentRunRepository(db), signer, "key-1")
			two, err := app.Send(ctx, "local-user", id, runTestMessage())
			if err != nil || !two.Durable || two.RunID != one.RunID || len(runtime.tokens) != 1 {
				t.Fatal(two, err)
			}
		})
	}
}
func TestAgentRunRetryKeepsOriginalAuthorityAndDeadline(t *testing.T) {
	app, _, settings, runtime, _, id := runAppFixture(t)
	ctx := context.Background()
	runtime.beforeDelivery = true
	one, err := app.Send(ctx, "local-user", id, runTestMessage())
	if err == nil || one.Durable {
		t.Fatal(one, err)
	}
	before, err := app.runs.Get(ctx, "local-user", one.RunID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := app.Send(ctx, "local-user", id, runTestMessage())
	if err != nil || two.RunID != one.RunID {
		t.Fatal(two, err)
	}
	after, err := app.runs.Get(ctx, "local-user", one.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ClaimsJSON != after.ClaimsJSON || before.DeadlineMS != after.DeadlineMS || len(runtime.tokens) != 2 || runtime.tokens[0] != runtime.tokens[1] {
		t.Fatal("retry renewed authority")
	}
	if _, err = settings.PatchLLMProvider(ctx, "fixture", service.LLMProviderPatch{APIKeyUpdate: &service.ModelCredentialUpdate{Action: "revoke"}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = app.Authorization(ctx, one.RunID); err == nil {
		t.Fatal("revoked model authorized")
	}
	if _, err = app.Send(ctx, "local-user", id, runTestMessage()); err != nil {
		t.Fatal("existing result required new authority", err)
	}
}
func TestAgentRunConcurrentCreationAndCancellation(t *testing.T) {
	app, _, _, runtime, db, id := runAppFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan AgentRunSubmission, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() { v, err := app.Send(ctx, "local-user", id, runTestMessage()); results <- v; failures <- err })
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var runID string
	for v := range results {
		if runID != "" && runID != v.RunID {
			t.Fatal("duplicate request binding")
		}
		runID = v.RunID
	}
	var count int64
	if err := db.Model(&domain.AgentRequestBinding{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if len(runtime.runs) != 1 {
		t.Fatal("duplicate prompt execution")
	}
	if _, err := app.Cancel(ctx, "local-user", runID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Authorization(ctx, runID); !errors.Is(err, service.ErrAgentRunRejected) {
		t.Fatal("cancel did not revoke", err)
	}
}
func TestAgentRunInputAndMissingArtifactFailClosed(t *testing.T) {
	app, _, _, runtime, _, id := runAppFixture(t)
	ctx := context.Background()
	for _, bad := range []service.AgentRunMessage{{}, {ClientRequestID: "bad.id", Content: runTestMessage().Content}, {ClientRequestID: "valid", Content: []agentbridge.TextContent{{Type: "image", Text: "x"}}}, {ClientRequestID: "valid", Content: []agentbridge.TextContent{{Type: "text", Text: strings.Repeat("中", 32001)}}}} {
		if _, err := app.Send(ctx, "local-user", id, bad); !errors.Is(err, service.ErrAgentRunInput) {
			t.Fatal(err)
		}
	}
	one, err := app.Send(ctx, "local-user", id, runTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	delete(runtime.runs, one.RunID)
	if _, err = app.Send(ctx, "local-user", id, runTestMessage()); !errors.Is(err, service.ErrAgentRunUnavailable) {
		t.Fatal("missing acknowledged artifact re-created", err)
	}
	if len(runtime.tokens) != 1 {
		t.Fatal("duplicate dispatch after artifact loss")
	}
}

func TestAgentRunExpiredOrCancelledPendingRequestCannotRedispatch(t *testing.T) {
	for _, reason := range []string{"expiry", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			app, _, _, runtime, _, id := runAppFixture(t)
			ctx := context.Background()
			profile := app.profiles["text_only"]
			profile.Budgets.WallTimeMS = 1000
			app.profiles["text_only"] = profile
			runtime.beforeDelivery = true
			one, err := app.Send(ctx, "local-user", id, runTestMessage())
			if err == nil || one.RunID == "" {
				t.Fatal(one, err)
			}
			row, err := app.runs.Get(ctx, "local-user", one.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "expiry" {
				time.Sleep(time.Until(time.UnixMilli(row.DeadlineMS)) + 20*time.Millisecond)
			} else {
				if _, err = app.Cancel(ctx, "local-user", one.RunID); !errors.Is(err, service.ErrAgentRunPending) {
					t.Fatal(err)
				}
			}
			_, err = app.Send(ctx, "local-user", id, runTestMessage())
			if !errors.Is(err, service.ErrAgentRunExpired) && !errors.Is(err, service.ErrAgentRunRejected) {
				t.Fatal("stopped request redispatched", err)
			}
			if len(runtime.tokens) != 1 || len(runtime.runs) != 0 {
				t.Fatal("new authority or execution after stop")
			}
		})
	}
}

func TestAgentRunEventsOwnershipBindingAndReadOnlyRecovery(t *testing.T) {
	app, _, _, runtime, db, session := runAppFixture(t)
	ctx := context.Background()
	sent, err := app.Send(ctx, "local-user", session, runTestMessage())
	if err != nil {
		t.Fatal(err)
	}
	called, accepted := 0, 0
	badBinding := false
	runtime.stream = func(_ context.Context, sid, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
		called++
		if sid != session || id != sent.RunID || cursor != "9007199254740993" {
			t.Fatal("stream binding changed")
		}
		run := runtime.runs[id]
		message := run.MessageID
		if badBinding {
			message = "another-message"
		}
		err := callbacks.Event(agentbridge.Event{Type: "run.started", Data: agentbridge.RunStartedData{MessageID: message, ExecutionEnvelopeDigest: run.ExecutionEnvelopeDigest}})
		return agentbridge.StreamResult{LastEventID: cursor}, err
	}
	callbacks := agentbridge.StreamCallbacks{Event: func(agentbridge.Event) error { accepted++; return nil }}
	for _, tc := range []struct {
		actor, cursor string
		want          error
	}{
		{"other-actor", "9007199254740993", service.ErrAgentRunNotFound},
		{"local-user", "01", service.ErrAgentRunCursorInvalid},
	} {
		if _, err := app.Events(ctx, tc.actor, sent.RunID, tc.cursor, callbacks); !errors.Is(err, tc.want) {
			t.Fatalf("wrong gate: %v", err)
		}
	}
	if called != 0 {
		t.Fatal("invalid/foreign subscription reached Runtime")
	}
	if err := db.Model(&domain.AgentRequestBinding{}).Where("id = ?", sent.RunID).Updates(map[string]any{"revoked": true}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Events(ctx, "local-user", sent.RunID, "9007199254740993", callbacks); err != nil {
		t.Fatal(err)
	}
	if called != 1 || accepted != 1 {
		t.Fatal("completed revoked Run must remain readable")
	}
	badBinding = true
	if _, err := app.Events(ctx, "local-user", sent.RunID, "9007199254740993", callbacks); !errors.Is(err, service.ErrAgentRunUnavailable) {
		t.Fatal(err)
	}
	if accepted != 1 {
		t.Fatal("mismatched envelope/message escaped to browser")
	}
	runtime.stream = func(context.Context, string, string, string, agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
		return agentbridge.StreamResult{}, &agentbridge.Error{Code: "agent_event_cursor_expired", Status: 410}
	}
	if _, err := app.Events(ctx, "local-user", sent.RunID, "9007199254740993", callbacks); !errors.Is(err, service.ErrAgentRunCursorExpired) {
		t.Fatal(err)
	}
}
