// Package application orchestrates event ingestion: loading the pipeline, driving the
// execution engine, and persisting the results (the event's final state, per-node
// lineage, and AI results).
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/pipeline"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

// ErrPipelineDisabled means the pipeline is not enabled, so the event is refused.
var ErrPipelineDisabled = errors.New("pipeline is not enabled")

type PipelineApp struct {
	pipelineRepo *repository.PipelineRepository
	eventRepo    *repository.EventRepository
	exec         *pipeline.Executor
}

func NewPipelineApp(pipelineRepo *repository.PipelineRepository, eventRepo *repository.EventRepository, reg *pipeline.Registry) *PipelineApp {
	return &PipelineApp{
		pipelineRepo: pipelineRepo,
		eventRepo:    eventRepo,
		exec:         pipeline.NewExecutor(reg),
	}
}

// Ingest takes one event, runs it through the named pipeline, and returns the event
// with its final state.
func (a *PipelineApp) Ingest(ctx context.Context, pipelineID int64, source string, payload map[string]any) (*domain.Event, error) {
	p, err := a.pipelineRepo.GetByID(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	if p.Status != domain.PipelineStatusEnabled {
		return nil, ErrPipelineDisabled
	}

	if payload == nil {
		payload = map[string]any{}
	}
	rawJSON, _ := sonic.Marshal(payload)

	evt := &domain.Event{
		EventUID:   newEventUID(),
		PipelineID: pipelineID,
		Source:     source,
		RawPayload: rawJSON,
		Status:     domain.EventStatusProcessing,
		ReceivedAt: time.Now(),
	}
	if err := a.eventRepo.Create(ctx, evt); err != nil {
		return nil, err
	}

	nodes, edges := service.ToEngine(p)
	msg := &pipeline.Message{
		ID:      evt.EventUID,
		Payload: clone(payload),
		Meta:    map[string]any{"source": source, "pipeline_id": pipelineID},
	}
	res := a.exec.Run(ctx, nodes, edges, msg, false)

	now := time.Now()
	evt.Status = res.Status
	evt.DroppedAtNode = res.DroppedAtNode
	evt.FinishedAt = &now
	if res.Err != nil {
		evt.Error = truncate(res.Err.Error(), 500)
	}
	if fp, mErr := sonic.Marshal(res.FinalPayload); mErr == nil {
		evt.FinalPayload = fp
	}

	traces := mapTraces(evt.ID, res.Traces)
	aiResults := mapAIResults(evt.ID, res.AIResults)

	if err := a.eventRepo.SaveResult(ctx, evt, traces, aiResults); err != nil {
		return nil, err
	}

	logger.L().Info("event processed",
		zap.String("uid", evt.EventUID),
		zap.Int64("pipeline_id", pipelineID),
		zap.String("status", evt.Status),
		zap.String("dropped_at", evt.DroppedAtNode),
	)
	return evt, nil
}

func (a *PipelineApp) ListEvents(ctx context.Context, q repository.EventQuery) ([]*domain.Event, int64, error) {
	return a.eventRepo.List(ctx, q)
}

func (a *PipelineApp) GetEvent(ctx context.Context, id int64) (*domain.Event, error) {
	return a.eventRepo.GetByID(ctx, id)
}

// ExportEvents fetches every event matching the filter and also returns a
// pipeline_id -> name map, so the export can render pipeline IDs as readable names.
func (a *PipelineApp) ExportEvents(ctx context.Context, q repository.EventQuery) ([]*domain.Event, map[int64]string, error) {
	events, err := a.eventRepo.ListAll(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	pipelines, err := a.pipelineRepo.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	names := make(map[int64]string, len(pipelines))
	for _, p := range pipelines {
		names[p.ID] = p.Name
	}
	return events, names, nil
}

func (a *PipelineApp) ListAIResults(ctx context.Context, eventID int64) ([]*domain.AIResult, error) {
	return a.eventRepo.ListAIResults(ctx, eventID)
}

func mapTraces(eventID int64, traces []pipeline.NodeTrace) []domain.EventTrace {
	out := make([]domain.EventTrace, 0, len(traces))
	for _, t := range traces {
		out = append(out, domain.EventTrace{
			EventID:   eventID,
			NodeKey:   t.NodeKey,
			NodeType:  t.NodeType,
			Action:    t.Action,
			LatencyMS: t.LatencyMS,
			Error:     truncate(t.Error, 500),
		})
	}
	return out
}

func mapAIResults(eventID int64, invs []pipeline.AIInvocation) []domain.AIResult {
	out := make([]domain.AIResult, 0, len(invs))
	for _, inv := range invs {
		out = append(out, domain.AIResult{
			EventID:          eventID,
			NodeKey:          inv.NodeKey,
			Provider:         inv.Provider,
			Model:            inv.Model,
			PromptSnapshot:   inv.PromptSnapshot,
			Output:           inv.Output,
			TokensPrompt:     inv.TokensPrompt,
			TokensCompletion: inv.TokensCompletion,
			LatencyMS:        inv.LatencyMS,
		})
	}
	return out
}

func newEventUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// A rand failure is vanishingly rare; fall back to a timestamp rather than block
		// ingestion.
		return "evt-" + time.Now().Format("20060102150405.000000")
	}
	return "evt-" + hex.EncodeToString(b)
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
