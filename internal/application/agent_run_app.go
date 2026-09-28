package application

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

type AgentRunApplication struct {
	settings        *service.SettingService
	sessions        *service.AgentSessionService
	runs            *service.AgentRunService
	runtime         *service.AgentRunRuntimeService
	manifest        config.AgentRunManifest
	profiles        map[string]config.AgentRunProfile
	revisions       map[string]string
	keyID           string
	tools           map[string][]string
	release         *service.AgentReleaseService
	releaseRequired bool
}
type AgentRunSubmission struct {
	RunID     string `json:"run_id"`
	Durable   bool   `json:"durable"`
	Status    string `json:"status"`
	RunURL    string `json:"run_url"`
	EventsURL string `json:"events_url"`
}

func NewAgentRunApplication(settings *service.SettingService, sessions *service.AgentSessionService, runs *service.AgentRunService, runtime *service.AgentRunRuntimeService, cfg *config.Config, catalogs ...*ToolCatalogApplication) (*AgentRunApplication, error) {
	if settings == nil || sessions == nil || runs == nil || runtime == nil || cfg.ValidateAgentRuns() != nil {
		return nil, config.ErrAgentRunsConfig
	}
	profiles := make(map[string]config.AgentRunProfile, len(cfg.Agent.Runs.Profiles))
	revisions := make(map[string]string, len(profiles))
	tools := make(map[string][]string)
	for id, p := range cfg.Agent.Runs.Profiles {
		if p.Budgets.MaxToolCalls > 0 {
			catalog, err := profileBootstrapCatalog(cfg, catalogs, id)
			if err != nil || catalog == nil {
				return nil, config.ErrAgentRunsConfig
			}
			p.ToolCatalogRevision = catalog.Revision
			for _, tool := range catalog.Tools {
				tools[id] = append(tools[id], tool.Name)
			}
		}
		profiles[id] = p
		revisions[id] = cfg.Agent.Sessions.Profiles[id]
	}
	return &AgentRunApplication{settings: settings, sessions: sessions, runs: runs, runtime: runtime, manifest: cfg.Agent.Runs.Manifest,
		profiles: profiles, revisions: revisions, keyID: cfg.Agent.Runs.SigningKeyID, tools: tools, releaseRequired: cfg.Agent.Runs.ReleaseFile != ""}, nil
}

// Set once during composition, before handlers or background loops are started.
func (a *AgentRunApplication) SetReleaseSource(source *service.AgentReleaseService) {
	a.release = source
}
func (a *AgentRunApplication) currentManifest(ctx context.Context) (config.AgentRunManifest, error) {
	if a.release != nil {
		return a.release.Current(ctx)
	}
	if a.releaseRequired {
		return config.AgentRunManifest{}, service.ErrAgentRunUnavailable
	}
	return a.manifest, nil
}
func submission(row domain.AgentRequestBinding) AgentRunSubmission {
	return AgentRunSubmission{RunID: row.ID, Status: "delivery_unconfirmed", RunURL: "/api/v1/agent/runs/" + row.ID, EventsURL: "/api/v1/agent/runs/" + row.ID + "/events"}
}
func (a *AgentRunApplication) Profiles() []string {
	ids := make([]string, 0, len(a.profiles))
	for id := range a.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (a *AgentRunApplication) TextOnly() bool {
	return len(a.tools) == 0
}
func (a *AgentRunApplication) Events(ctx context.Context, actor, id, cursor string, callbacks agentbridge.StreamCallbacks) (agentbridge.StreamResult, error) {
	if agentbridge.ValidateEventCursor(cursor) != nil {
		return agentbridge.StreamResult{}, service.ErrAgentRunCursorInvalid
	}
	// Validate current ownership and the durable Runtime binding before writing
	// browser headers. Reading a completed/expired Run never requires execution authority.
	run, err := a.Get(ctx, actor, id)
	if err != nil {
		return agentbridge.StreamResult{}, err
	}
	accept := callbacks.Event
	callbacks.Event = func(event agentbridge.Event) error {
		if event.Type == "run.started" {
			data, ok := event.Data.(agentbridge.RunStartedData)
			if !ok || data.MessageID != run.MessageID || data.ExecutionEnvelopeDigest != run.ExecutionEnvelopeDigest {
				return service.ErrAgentRunUnavailable
			}
		}
		return accept(event)
	}
	if accept == nil {
		return agentbridge.StreamResult{}, service.ErrAgentRunInput
	}
	return a.runtime.Events(ctx, run.SessionID, id, cursor, callbacks)
}
func (a *AgentRunApplication) envelope(ctx context.Context, session domain.AgentSessionBinding) (agentrunauth.Envelope, error) {
	p, ok := a.profiles[session.Profile]
	if !ok {
		return agentrunauth.Envelope{}, service.ErrAgentSessionProfile
	}
	catalog, err := a.settings.GetAgentModelCatalog(ctx)
	if err != nil {
		return agentrunauth.Envelope{}, err
	}
	ready := false
	for _, model := range catalog.Models {
		if model.Provider == session.Provider && model.Model == session.Model && model.Status == "ready" {
			ready = true
		}
	}
	if !ready {
		return agentrunauth.Envelope{}, service.ErrAgentSessionModel
	}
	manifest, err := a.currentManifest(ctx)
	if err != nil {
		return agentrunauth.Envelope{}, service.ErrAgentRunUnavailable
	}
	e := manifest.Envelope(p)
	e.ProductProfile, e.ProfileRevision = session.Profile, a.revisions[session.Profile]
	e.Provider, e.Model, e.ModelConfigRevision = session.Provider, session.Model, catalog.Revision
	return e, nil
}

// Authorization is a trusted, authenticated control-plane read. Every grant is
// derived from the original record and current policy; no browser claims enter it.
func (a *AgentRunApplication) Authorization(ctx context.Context, id string) (agentrunauth.Claims, error) {
	row, err := a.runs.PolicyRecord(ctx, id)
	if err != nil {
		return agentrunauth.Claims{}, err
	}
	if row.Revoked || row.SigningKeyID != a.keyID {
		return agentrunauth.Claims{}, service.ErrAgentRunRejected
	}
	if time.Now().UnixMilli() >= row.DeadlineMS {
		return agentrunauth.Claims{}, service.ErrAgentRunExpired
	}
	session, err := a.sessions.Get(ctx, row.ActorID, row.SessionID)
	if err != nil {
		return agentrunauth.Claims{}, err
	}
	// Archive is product metadata: it blocks new admission, but does not cancel
	// previously authorized work. Explicit cancellation durably revokes the grant.
	if (session.Status != domain.AgentSessionActive && session.Status != domain.AgentSessionArchived) || session.DSHSessionID == nil || *session.DSHSessionID != session.ID {
		return agentrunauth.Claims{}, service.ErrAgentRunRejected
	}
	expected, err := a.envelope(ctx, session)
	if err != nil {
		return agentrunauth.Claims{}, err
	}
	claims, err := service.AgentRunClaims(row)
	if err != nil {
		return agentrunauth.Claims{}, err
	}
	expected.RunID, expected.ClientRequestID, expected.ActorID = row.ID, claims.Envelope.ClientRequestID, row.ActorID
	if expected != claims.Envelope || !slices.Equal(claims.AllowedTools, a.tools[session.Profile]) {
		return agentrunauth.Claims{}, service.ErrAgentRunRejected
	}
	return claims, nil
}
func (a *AgentRunApplication) Send(ctx context.Context, actor, sessionID string, input service.AgentRunMessage) (AgentRunSubmission, error) {
	if _, err := service.AgentRunHash(sessionID, input); err != nil {
		return AgentRunSubmission{}, err
	}
	session, err := a.sessions.Get(ctx, actor, sessionID)
	if err != nil {
		return AgentRunSubmission{}, err
	}
	row, err := a.runs.Find(ctx, actor, sessionID, input)
	if errors.Is(err, service.ErrAgentRunNotFound) {
		if session.Status != domain.AgentSessionActive || session.DSHSessionID == nil || *session.DSHSessionID != sessionID {
			return AgentRunSubmission{}, service.ErrAgentSessionState
		}
		e, err := a.envelope(ctx, session)
		if err != nil {
			return AgentRunSubmission{}, err
		}
		row, err = a.runs.Prepare(ctx, session, input, e, a.tools[session.Profile]...)
		if err != nil {
			return AgentRunSubmission{}, err
		}
	} else if err != nil {
		return AgentRunSubmission{}, err
	}
	result := submission(row)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Query before redispatch: this also repairs a lost reply or database commit
	// without requiring an expired/revoked Capability to authorize new work.
	projection, err := a.runtime.Get(ctx, row.SessionID, row.ID)
	if err == nil {
		if err = a.confirmProjection(ctx, row, projection); err != nil {
			return result, err
		}
		result.Durable, result.Status = true, projection.State
		return result, nil
	}
	if !errors.Is(err, service.ErrAgentRunNotFound) {
		return result, err
	}
	if row.MessageID != nil {
		return result, service.ErrAgentRunUnavailable
	}
	if _, err = a.Authorization(ctx, row.ID); err != nil {
		return result, err
	}
	token, err := a.runs.Token(row)
	if err != nil {
		return result, err
	}
	accepted, err := a.runtime.Prompt(ctx, agentbridge.PromptRequest{SessionID: row.SessionID, RunID: row.ID, ClientRequestID: input.ClientRequestID, RequestHash: row.RequestHash, ExecutionEnvelopeDigest: row.EnvelopeDigest, RunCapability: token, Content: input.Content})
	if err != nil {
		return result, err
	}
	if !accepted.Durable || accepted.RunID != row.ID || accepted.State != "accepted" {
		return result, service.ErrAgentRunUnavailable
	}
	if err = a.confirm(ctx, row, accepted.MessageID); err != nil {
		return result, err
	}
	result.Durable, result.Status = true, "accepted"
	return result, nil
}
func (a *AgentRunApplication) confirm(ctx context.Context, row domain.AgentRequestBinding, messageID string) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return a.runs.Acknowledge(finish, row, messageID)
}
func (a *AgentRunApplication) confirmProjection(ctx context.Context, row domain.AgentRequestBinding, v agentbridge.Run) error {
	if v.RunID != row.ID || v.SessionID != row.SessionID || v.ExecutionEnvelopeDigest != row.EnvelopeDigest {
		return service.ErrAgentRunUnavailable
	}
	if !v.Durable {
		return service.ErrAgentRunPending
	}
	return a.confirm(ctx, row, v.MessageID)
}
func (a *AgentRunApplication) Get(ctx context.Context, actor, id string) (agentbridge.Run, error) {
	row, err := a.runs.Get(ctx, actor, id)
	if err != nil {
		return agentbridge.Run{}, err
	}
	value, err := a.runtime.Get(ctx, row.SessionID, id)
	if errors.Is(err, service.ErrAgentRunNotFound) {
		return agentbridge.Run{}, service.ErrAgentRunPending
	}
	if err != nil {
		return agentbridge.Run{}, err
	}
	return value, a.confirmProjection(ctx, row, value)
}
func (a *AgentRunApplication) Cancel(ctx context.Context, actor, id string) (agentbridge.Run, error) {
	row, err := a.runs.Get(ctx, actor, id)
	if err != nil {
		return agentbridge.Run{}, err
	}
	if err = a.runs.Revoke(ctx, row); err != nil {
		return agentbridge.Run{}, err
	}
	value, err := a.runtime.Cancel(ctx, row.SessionID, id)
	if errors.Is(err, service.ErrAgentRunNotFound) {
		return agentbridge.Run{}, service.ErrAgentRunPending
	}
	if err != nil {
		return agentbridge.Run{}, err
	}
	return value, a.confirmProjection(ctx, row, value)
}
