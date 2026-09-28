package application

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/quant4dad/internal/agentbridge"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

type AgentSessionApplication struct {
	settings *service.SettingService
	sessions *service.AgentSessionService
	runtime  *service.AgentSessionRuntimeService
	profiles map[string]string
}
type AgentSessionDetail struct {
	Session    service.AgentSessionView    `json:"session"`
	Transcript *agentbridge.TranscriptPage `json:"transcript"`
}

func NewAgentSessionApplication(settings *service.SettingService, sessions *service.AgentSessionService, runtime *service.AgentSessionRuntimeService, profiles map[string]string) (*AgentSessionApplication, error) {
	if settings == nil || sessions == nil || runtime == nil || len(profiles) < 1 || len(profiles) > 128 {
		return nil, service.ErrAgentSessionProfile
	}
	frozen := make(map[string]string, len(profiles))
	for id, revision := range profiles {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`).MatchString(id) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(revision) {
			return nil, service.ErrAgentSessionProfile
		}
		frozen[id] = revision
	}
	return &AgentSessionApplication{settings, sessions, runtime, frozen}, nil
}
func (a *AgentSessionApplication) Create(ctx context.Context, actor, key string, request service.AgentSessionCreate) (service.AgentSessionView, error) {
	row, err := a.sessions.Find(ctx, actor, key, request)
	if errors.Is(err, service.ErrAgentSessionNotFound) {
		revision, allowed := a.profiles[request.Profile]
		if !allowed {
			return service.AgentSessionView{}, service.ErrAgentSessionProfile
		}
		catalog, e := a.settings.GetAgentModelCatalog(ctx)
		if e != nil {
			return service.AgentSessionView{}, e
		}
		ready := false
		for _, model := range catalog.Models {
			if model.Provider == request.Provider && model.Model == request.Model && model.Status == "ready" {
				ready = true
			}
		}
		if !ready {
			return service.AgentSessionView{}, service.ErrAgentSessionModel
		}
		row, err = a.sessions.Prepare(ctx, actor, key, request, revision, catalog.Revision)
	}
	if err != nil {
		return service.AgentSessionView{}, err
	}
	return a.provision(ctx, row)
}
func (a *AgentSessionApplication) Reconcile(ctx context.Context, actor, id string) (service.AgentSessionView, error) {
	row, err := a.sessions.Get(ctx, actor, id)
	if err != nil {
		return service.AgentSessionView{}, err
	}
	return a.provision(ctx, row)
}
func (a *AgentSessionApplication) provision(ctx context.Context, row domain.AgentSessionBinding) (service.AgentSessionView, error) {
	if row.Status == domain.AgentSessionActive || row.Status == domain.AgentSessionArchived {
		return service.SessionView(row), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	attempt, claimed, err := a.sessions.Claim(ctx, row)
	if err != nil {
		return service.AgentSessionView{}, err
	}
	if claimed {
		// Existing Bridge create is an idempotent reconciliation operation. It
		// verifies the durable original binding/header before acknowledging, and
		// refuses orphan adoption or recreation of a missing acknowledged artifact.
		result, code := a.runtime.Provision(ctx, service.SessionBridgeRequest(row))
		finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		if code == "" {
			err = a.sessions.Complete(finish, row, attempt, &result, "")
		} else {
			err = a.sessions.Complete(finish, row, attempt, nil, code)
		}
		cancelFinish()
		if err != nil {
			return service.AgentSessionView{}, err
		}
	}
	// Preserve the recoverable response after the Runtime deadline or browser
	// cancellation; the product intent and failure have already been committed.
	read, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancelRead()
	row, err = a.sessions.Get(read, row.ActorID, row.ID)
	return service.SessionView(row), err
}
func (a *AgentSessionApplication) List(ctx context.Context, actor, cursor string, limit int) (service.AgentSessionPage, error) {
	return a.sessions.List(ctx, actor, cursor, limit)
}
func (a *AgentSessionApplication) Patch(ctx context.Context, actor, id string, patch service.AgentSessionPatch) (service.AgentSessionView, error) {
	return a.sessions.Patch(ctx, actor, id, patch)
}
func (a *AgentSessionApplication) Metadata(ctx context.Context, actor, id string) (service.AgentSessionView, error) {
	row, err := a.sessions.Get(ctx, actor, id)
	return service.SessionView(row), err
}
func (a *AgentSessionApplication) Detail(ctx context.Context, actor, id string, query agentbridge.TranscriptQuery) (AgentSessionDetail, error) {
	if query.Validate() != nil {
		return AgentSessionDetail{}, service.ErrAgentSessionInput
	}
	row, err := a.sessions.Get(ctx, actor, id)
	if err != nil {
		return AgentSessionDetail{}, err
	}
	out := AgentSessionDetail{Session: service.SessionView(row)}
	if row.Status != domain.AgentSessionActive && row.Status != domain.AgentSessionArchived {
		return out, nil
	}
	if row.DSHSessionID == nil || *row.DSHSessionID != row.ID {
		return out, service.ErrAgentSessionStore
	}
	page, err := a.runtime.Transcript(ctx, *row.DSHSessionID, query)
	if err != nil {
		return out, err
	}
	out.Transcript = &page
	return out, nil
}

// ReconcileBatch is bounded and serial. Database leases coordinate multiple API
// instances. It touches only existing product bindings, never enumerates/deletes
// unowned DSH artifacts. Caller cancellation stops subsequent work.
func (a *AgentSessionApplication) ReconcileBatch(ctx context.Context) error {
	rows, err := a.sessions.Due(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := a.provision(ctx, row); err != nil {
			return err
		}
	}
	return nil
}
func (a *AgentSessionApplication) StartReconciler() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			_ = a.ReconcileBatch(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
